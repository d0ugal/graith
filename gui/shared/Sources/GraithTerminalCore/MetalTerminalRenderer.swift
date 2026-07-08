import CoreGraphics
import CoreText
import Metal
import MetalKit
import CGhosttyVT

// GPU-accelerated terminal renderer, shared by macOS and iOS.
//
// Ported off AppKit: glyph rasterization uses CoreText (`CTFont`) throughout
// instead of `NSFont`/`NSFontManager`/`NSColor`, and textures use `.shared`
// storage (works on both platforms' unified-memory GPUs). The only
// platform-specific input is the initial ``PlatformFont`` (see Platform.swift).
// Metal + MetalKit (`MTKView`/`MTKViewDelegate`) are available on both
// platforms, so the renderer and its shaders are unchanged.

// MARK: - Cell Instance (GPU buffer layout)

struct CellInstance {
    var posX: Float
    var posY: Float
    var fgR: Float
    var fgG: Float
    var fgB: Float
    var fgA: Float
    var bgR: Float
    var bgG: Float
    var bgB: Float
    var bgA: Float
    var glyphIndex: UInt16
    var flags: UInt16
}

struct CellUniforms {
    var viewportWidth: Float
    var viewportHeight: Float
    var cellWidth: Float
    var cellHeight: Float
    var atlasWidth: Float
    var atlasHeight: Float
    var glyphWidth: Float
    var glyphHeight: Float
    var glyphsPerRow: UInt32
    var padding: UInt32 = 0
}

// MARK: - Font Atlas

/// Dynamic glyph atlas supporting arbitrary Unicode codepoints and bold/italic
/// variants. Pre-renders printable ASCII, then caches glyphs on demand into an
/// R8 texture laid out in a grid.
final class FontAtlas {
    private(set) var texture: MTLTexture
    let glyphWidth: CGFloat
    let glyphHeight: CGFloat
    private(set) var glyphsPerRow: Int
    let cellDescent: CGFloat

    private let device: MTLDevice
    private let baseFont: CTFont
    private let boldFont: CTFont
    private let italicFont: CTFont
    private let boldItalicFont: CTFont

    struct GlyphKey: Hashable {
        let codepoints: [UInt32]
        let bold: Bool
        let italic: Bool
    }

    private var glyphCache: [GlyphKey: UInt16] = [:]
    private var nextSlot: Int = 0
    private var atlasCapacity: Int = 0
    private var atlasRows: Int = 0
    private let atlasCols = 32
    private let colorSpace = CGColorSpaceCreateDeviceGray()
    private let glyphWhite: CGColor

    init(font: PlatformFont, device: MTLDevice) {
        self.device = device
        let base = TerminalFontProvider.ctFont(from: font)
        self.baseFont = base
        self.boldFont = TerminalFontProvider.variant(of: base, bold: true, italic: false)
        self.italicFont = TerminalFontProvider.variant(of: base, bold: false, italic: true)
        self.boldItalicFont = TerminalFontProvider.variant(of: base, bold: true, italic: true)

        let ascent = CTFontGetAscent(base)
        let descent = CTFontGetDescent(base)
        let leading = CTFontGetLeading(base)

        // Advance width of 'M' via CoreText (replaces NSString.size).
        let chars: [UniChar] = Array("M".utf16)
        var glyphs = [CGGlyph](repeating: 0, count: chars.count)
        CTFontGetGlyphsForCharacters(base, chars, &glyphs, chars.count)
        var advance = CGSize.zero
        CTFontGetAdvancesForGlyphs(base, .horizontal, &glyphs, &advance, 1)

        let gw = ceil(advance.width)
        let gh = ceil(ascent + descent + leading)
        self.glyphWidth = gw
        self.glyphHeight = gh
        self.cellDescent = ceil(descent)
        self.glyphsPerRow = atlasCols
        self.glyphWhite = CGColor(colorSpace: colorSpace, components: [1.0, 1.0])
            ?? CGColor(gray: 1.0, alpha: 1.0)

        let initialCapacity = 95 + 256
        atlasRows = (initialCapacity + atlasCols - 1) / atlasCols
        atlasCapacity = atlasRows * atlasCols

        let atlasPixelWidth = Int(gw) * atlasCols
        let atlasPixelHeight = Int(gh) * atlasRows

        let desc = MTLTextureDescriptor.texture2DDescriptor(
            pixelFormat: .r8Unorm,
            width: atlasPixelWidth,
            height: atlasPixelHeight,
            mipmapped: false
        )
        desc.usage = [.shaderRead]
        desc.storageMode = .shared
        guard let tex = device.makeTexture(descriptor: desc) else {
            fatalError("FontAtlas: failed to create atlas texture")
        }
        self.texture = tex

        for cp: UInt32 in 0x20...0x7E {
            guard let scalar = UnicodeScalar(cp) else { continue }
            let key = GlyphKey(codepoints: [cp], bold: false, italic: false)
            let slot = nextSlot
            glyphCache[key] = UInt16(slot)
            nextSlot += 1
            renderGlyph(String(scalar), font: baseFont, slot: slot)
        }
    }

    func glyphIndex(for codepoints: [UInt32], bold: Bool, italic: Bool) -> UInt16 {
        let key = GlyphKey(codepoints: codepoints, bold: bold, italic: italic)
        if let idx = glyphCache[key] { return idx }

        if (bold || italic), let cp = codepoints.first, cp >= 0x20, cp <= 0x7E {
            let unstyledKey = GlyphKey(codepoints: codepoints, bold: false, italic: false)
            if glyphCache[unstyledKey] != nil {
                return renderAndCache(codepoints: codepoints, bold: bold, italic: italic)
            }
        }

        return renderAndCache(codepoints: codepoints, bold: bold, italic: italic)
    }

    private func renderAndCache(codepoints: [UInt32], bold: Bool, italic: Bool) -> UInt16 {
        let key = GlyphKey(codepoints: codepoints, bold: bold, italic: italic)
        if let idx = glyphCache[key] { return idx }

        if nextSlot >= atlasCapacity {
            growAtlas()
        }

        let slot = nextSlot
        nextSlot += 1

        let str = String(codepoints.compactMap { UnicodeScalar($0) }.map { Character($0) })
        renderGlyph(str, font: fontFor(bold: bold, italic: italic), slot: slot)
        let idx = UInt16(slot)
        glyphCache[key] = idx
        return idx
    }

    private func fontFor(bold: Bool, italic: Bool) -> CTFont {
        switch (bold, italic) {
        case (true, true): return boldItalicFont
        case (true, false): return boldFont
        case (false, true): return italicFont
        case (false, false): return baseFont
        }
    }

    private func renderGlyph(_ str: String, font: CTFont, slot: Int) {
        let bw = Int(glyphWidth)
        let bh = Int(glyphHeight)

        guard let ctx = CGContext(
            data: nil, width: bw, height: bh,
            bitsPerComponent: 8, bytesPerRow: bw,
            space: colorSpace,
            bitmapInfo: CGImageAlphaInfo.none.rawValue
        ) else { return }

        ctx.setFillColor(gray: 0, alpha: 1)
        ctx.fill(CGRect(x: 0, y: 0, width: bw, height: bh))

        // Build the styled run entirely in CoreText (no NSAttributedString /
        // NSColor), so this is identical on macOS and iOS.
        let attrs: [CFString: Any] = [
            kCTFontAttributeName: font,
            kCTForegroundColorAttributeName: glyphWhite,
        ]
        guard let attrStr = CFAttributedStringCreate(nil, str as CFString, attrs as CFDictionary) else { return }
        let line = CTLineCreateWithAttributedString(attrStr)
        ctx.textPosition = CGPoint(x: 0, y: cellDescent)
        CTLineDraw(line, ctx)

        guard let data = ctx.data else { return }

        let col = slot % atlasCols
        let row = slot / atlasCols
        let region = MTLRegion(
            origin: MTLOrigin(x: col * bw, y: row * bh, z: 0),
            size: MTLSize(width: bw, height: bh, depth: 1)
        )
        texture.replace(region: region, mipmapLevel: 0, withBytes: data, bytesPerRow: bw)
    }

    private func growAtlas() {
        let newRows = atlasRows * 2
        let newCapacity = newRows * atlasCols
        let bw = Int(glyphWidth)
        let bh = Int(glyphHeight)

        let desc = MTLTextureDescriptor.texture2DDescriptor(
            pixelFormat: .r8Unorm,
            width: bw * atlasCols,
            height: bh * newRows,
            mipmapped: false
        )
        desc.usage = [.shaderRead]
        desc.storageMode = .shared
        guard let newTex = device.makeTexture(descriptor: desc) else { return }

        let zeroRow = [UInt8](repeating: 0, count: bw * atlasCols)
        for r in 0..<(bh * newRows) {
            let region = MTLRegion(
                origin: MTLOrigin(x: 0, y: r, z: 0),
                size: MTLSize(width: bw * atlasCols, height: 1, depth: 1)
            )
            newTex.replace(region: region, mipmapLevel: 0, withBytes: zeroRow, bytesPerRow: bw * atlasCols)
        }

        let oldTexture = texture
        texture = newTex
        atlasRows = newRows
        atlasCapacity = newCapacity

        for (key, slot) in glyphCache {
            let str = String(key.codepoints.compactMap { UnicodeScalar($0) }.map { Character($0) })
            renderGlyph(str, font: fontFor(bold: key.bold, italic: key.italic), slot: Int(slot))
        }
        _ = oldTexture
    }

    static var missingGlyphIndex: UInt16 { 0 } // space
}

// MARK: - Metal Shaders (as string constants)

private let metalShaderSource = """
#include <metal_stdlib>
using namespace metal;

struct CellInstance {
    float posX;
    float posY;
    float fgR;
    float fgG;
    float fgB;
    float fgA;
    float bgR;
    float bgG;
    float bgB;
    float bgA;
    ushort glyphIndex;
    ushort flags;
};

struct CellUniforms {
    float  viewportWidth;
    float  viewportHeight;
    float  cellWidth;
    float  cellHeight;
    float  atlasWidth;
    float  atlasHeight;
    float  glyphWidth;
    float  glyphHeight;
    uint   glyphsPerRow;
    uint   padding;
};

struct VertexOut {
    float4 position [[position]];
    float2 texCoord;
    float4 fgColor;
    float4 bgColor;
    uint   flags;
    float  cellLocalY;
};

vertex VertexOut vertex_cell(
    uint vertexID [[vertex_id]],
    uint instanceID [[instance_id]],
    const device CellInstance* instances [[buffer(0)]],
    constant CellUniforms& uniforms [[buffer(1)]]
) {
    const float2 corners[4] = {
        float2(0.0, 0.0),
        float2(1.0, 0.0),
        float2(0.0, 1.0),
        float2(1.0, 1.0),
    };
    const uint indices[6] = {0, 1, 2, 2, 1, 3};

    uint idx = indices[vertexID];
    float2 corner = corners[idx];

    CellInstance inst = instances[instanceID];
    uint glyphIdx = uint(inst.glyphIndex);
    uint flags = uint(inst.flags);

    float2 pos = float2(inst.posX, inst.posY) + corner * float2(uniforms.cellWidth, uniforms.cellHeight);

    float2 ndc;
    ndc.x = (pos.x / uniforms.viewportWidth) * 2.0 - 1.0;
    ndc.y = (pos.y / uniforms.viewportHeight) * 2.0 - 1.0;

    uint glyphCol = glyphIdx % uniforms.glyphsPerRow;
    uint glyphRow = glyphIdx / uniforms.glyphsPerRow;

    float2 texOrigin = float2(
        float(glyphCol) * uniforms.glyphWidth / uniforms.atlasWidth,
        float(glyphRow) * uniforms.glyphHeight / uniforms.atlasHeight
    );
    float2 texSize = float2(
        uniforms.glyphWidth / uniforms.atlasWidth,
        uniforms.glyphHeight / uniforms.atlasHeight
    );

    float2 uv = texOrigin + float2(corner.x, 1.0 - corner.y) * texSize;

    VertexOut out;
    out.position = float4(ndc, 0.0, 1.0);
    out.texCoord = uv;
    out.fgColor = float4(inst.fgR, inst.fgG, inst.fgB, inst.fgA);
    out.bgColor = float4(inst.bgR, inst.bgG, inst.bgB, inst.bgA);
    out.flags = flags;
    out.cellLocalY = corner.y;
    return out;
}

fragment float4 fragment_cell(
    VertexOut in [[stage_in]],
    texture2d<float> atlas [[texture(0)]],
    constant CellUniforms& uniforms [[buffer(1)]]
) {
    constexpr sampler s(mag_filter::nearest, min_filter::nearest);

    float glyphAlpha = atlas.sample(s, in.texCoord).r;

    float4 color = mix(in.bgColor, in.fgColor, glyphAlpha);

    if ((in.flags & 0x04u) != 0) {
        float threshold = 2.0 / uniforms.cellHeight;
        if (in.cellLocalY < threshold) {
            color = in.fgColor;
        }
    }

    if ((in.flags & 0x08u) != 0) {
        float halfPx = 1.0 / uniforms.cellHeight;
        if (abs(in.cellLocalY - 0.5) < halfPx) {
            color = in.fgColor;
        }
    }

    return color;
}
"""

private let cursorShaderSource = """
#include <metal_stdlib>
using namespace metal;

struct CursorInstance {
    float posX;
    float posY;
    float colorR;
    float colorG;
    float colorB;
    float colorA;
    float cellWidth;
    float cellHeight;
    uint  style;
    uint  padding;
};

struct CursorUniforms {
    float viewportWidth;
    float viewportHeight;
};

struct CursorVertexOut {
    float4 position [[position]];
    float4 color;
    float2 localUV;
    uint   style;
};

vertex CursorVertexOut vertex_cursor(
    uint vertexID [[vertex_id]],
    uint instanceID [[instance_id]],
    const device CursorInstance* instances [[buffer(0)]],
    constant CursorUniforms& uniforms [[buffer(1)]]
) {
    const float2 corners[4] = {
        float2(0.0, 0.0),
        float2(1.0, 0.0),
        float2(0.0, 1.0),
        float2(1.0, 1.0),
    };
    const uint indices[6] = {0, 1, 2, 2, 1, 3};

    uint idx = indices[vertexID];
    float2 corner = corners[idx];

    CursorInstance inst = instances[instanceID];

    float2 origin = float2(inst.posX, inst.posY);
    float2 size = float2(inst.cellWidth, inst.cellHeight);

    if (inst.style == 1) {
        size.x = 2.0;
    } else if (inst.style == 2) {
        size.y = 2.0;
    }

    float2 pos = origin + corner * size;

    float2 ndc;
    ndc.x = (pos.x / uniforms.viewportWidth) * 2.0 - 1.0;
    ndc.y = (pos.y / uniforms.viewportHeight) * 2.0 - 1.0;

    CursorVertexOut out;
    out.position = float4(ndc, 0.0, 1.0);
    out.color = float4(inst.colorR, inst.colorG, inst.colorB, inst.colorA);
    out.localUV = corner;
    out.style = inst.style;
    return out;
}

fragment float4 fragment_cursor(
    CursorVertexOut in [[stage_in]]
) {
    if (in.style == 0) {
        return float4(in.color.rgb, 0.5);
    } else if (in.style == 3) {
        float borderX = step(in.localUV.x, 0.05) + step(0.95, in.localUV.x);
        float borderY = step(in.localUV.y, 0.05) + step(0.95, in.localUV.y);
        float border = clamp(borderX + borderY, 0.0, 1.0);
        if (border < 0.5) { discard_fragment(); }
        return in.color;
    } else {
        return in.color;
    }
}
"""

// MARK: - Metal Terminal Renderer

/// GPU-accelerated terminal renderer using instanced quad drawing.
public final class MetalTerminalRenderer: NSObject, MTKViewDelegate {
    private let device: MTLDevice
    private let commandQueue: MTLCommandQueue
    private let cellPipelineState: MTLRenderPipelineState
    private let cursorPipelineState: MTLRenderPipelineState
    private let fontAtlas: FontAtlas

    public let cellWidth: CGFloat
    public let cellHeight: CGFloat
    public let cellDescent: CGFloat

    private var instanceBuffer: MTLBuffer?
    private var instanceBufferCapacity: Int = 0
    private var instanceCount: Int = 0

    private var cursorBuffer: MTLBuffer?
    private var cursorBufferCapacity: Int = 0
    private var hasCursor: Bool = false

    private var cellUniformBuffer: MTLBuffer?
    private var cursorUniformBuffer: MTLBuffer?

    private var viewportSize: CGSize = .zero
    private var backingScaleFactor: CGFloat = 1.0

    public private(set) var gridCols: Int = 80
    public private(set) var gridRows: Int = 24

    private var defaultFG: (Float, Float, Float) = (0.804, 0.839, 0.957)
    private var defaultBG: (Float, Float, Float) = (0.118, 0.118, 0.180)

    public init(device: MTLDevice, font: PlatformFont = TerminalFontProvider.monospaced(ofSize: 13)) {
        self.device = device
        guard let queue = device.makeCommandQueue() else {
            fatalError("MetalTerminalRenderer: failed to create command queue")
        }
        self.commandQueue = queue

        self.fontAtlas = FontAtlas(font: font, device: device)
        self.cellWidth = fontAtlas.glyphWidth
        self.cellHeight = fontAtlas.glyphHeight
        self.cellDescent = fontAtlas.cellDescent

        let cellPipeline = MetalTerminalRenderer.buildPipeline(
            device: device, shaderSource: metalShaderSource,
            vertexFunction: "vertex_cell", fragmentFunction: "fragment_cell",
            label: "CellPipeline"
        )
        self.cellPipelineState = cellPipeline

        let cursorPipeline = MetalTerminalRenderer.buildPipeline(
            device: device, shaderSource: cursorShaderSource,
            vertexFunction: "vertex_cursor", fragmentFunction: "fragment_cursor",
            label: "CursorPipeline"
        )
        self.cursorPipelineState = cursorPipeline

        super.init()
    }

    private static func buildPipeline(
        device: MTLDevice, shaderSource: String,
        vertexFunction: String, fragmentFunction: String, label: String
    ) -> MTLRenderPipelineState {
        let library: MTLLibrary
        do {
            library = try device.makeLibrary(source: shaderSource, options: nil)
        } catch {
            fatalError("MetalTerminalRenderer: failed to compile \(label) shaders: \(error)")
        }

        guard let vertexFn = library.makeFunction(name: vertexFunction),
              let fragmentFn = library.makeFunction(name: fragmentFunction) else {
            fatalError("MetalTerminalRenderer: shader functions not found in \(label)")
        }

        let desc = MTLRenderPipelineDescriptor()
        desc.label = label
        desc.vertexFunction = vertexFn
        desc.fragmentFunction = fragmentFn
        desc.colorAttachments[0].pixelFormat = .bgra8Unorm
        desc.colorAttachments[0].isBlendingEnabled = true
        desc.colorAttachments[0].sourceRGBBlendFactor = .sourceAlpha
        desc.colorAttachments[0].destinationRGBBlendFactor = .oneMinusSourceAlpha
        desc.colorAttachments[0].sourceAlphaBlendFactor = .one
        desc.colorAttachments[0].destinationAlphaBlendFactor = .oneMinusSourceAlpha

        do {
            return try device.makeRenderPipelineState(descriptor: desc)
        } catch {
            fatalError("MetalTerminalRenderer: failed to create \(label) pipeline state: \(error)")
        }
    }

    public func calculateGridSize(for size: CGSize) -> (cols: Int, rows: Int) {
        let cols = max(1, Int(floor(size.width / cellWidth)))
        let rows = max(1, Int(floor(size.height / cellHeight)))
        return (cols, rows)
    }

    public func setBackingScaleFactor(_ scale: CGFloat) {
        backingScaleFactor = scale
    }

    public struct CursorInfo {
        public let x: Int
        public let y: Int
        public let style: UInt32
        public let colorR: Float
        public let colorG: Float
        public let colorB: Float
    }

    public struct TerminalColors {
        public let fgR: Float
        public let fgG: Float
        public let fgB: Float
        public let bgR: Float
        public let bgG: Float
        public let bgB: Float
        public let cursorR: Float
        public let cursorG: Float
        public let cursorB: Float
        public let cursorHasValue: Bool

        public init(fgR: Float, fgG: Float, fgB: Float, bgR: Float, bgG: Float, bgB: Float,
                    cursorR: Float, cursorG: Float, cursorB: Float, cursorHasValue: Bool) {
            self.fgR = fgR; self.fgG = fgG; self.fgB = fgB
            self.bgR = bgR; self.bgG = bgG; self.bgB = bgB
            self.cursorR = cursorR; self.cursorG = cursorG; self.cursorB = cursorB
            self.cursorHasValue = cursorHasValue
        }
    }

    public func update(
        cells: [[GhosttyTerminalState.CellInfo]],
        rows: Int,
        cols: Int,
        colors: TerminalColors,
        cursor: CursorInfo?,
        selectedCells: Set<Int>? = nil,
        highlightedCells: Set<Int>? = nil,
        currentMatchCells: Set<Int>? = nil
    ) {
        self.gridRows = rows
        self.gridCols = cols
        self.defaultFG = (colors.fgR, colors.fgG, colors.fgB)
        self.defaultBG = (colors.bgR, colors.bgG, colors.bgB)

        var instances: [CellInstance] = []
        instances.reserveCapacity(rows * cols)

        let scale = Float(backingScaleFactor)
        let cw = Float(cellWidth) * scale
        let ch = Float(cellHeight) * scale
        let vpHeight = Float(viewportSize.height)

        for rowIndex in 0..<rows {
            let y = vpHeight - Float(rowIndex + 1) * ch
            let rowCells = rowIndex < cells.count ? cells[rowIndex] : []

            for colIndex in 0..<cols {
                let x = Float(colIndex) * cw

                let cell: GhosttyTerminalState.CellInfo? = colIndex < rowCells.count ? rowCells[colIndex] : nil

                var glyphIdx: UInt16 = FontAtlas.missingGlyphIndex
                var flags: UInt16 = 0

                var fgR = colors.fgR, fgG = colors.fgG, fgB = colors.fgB
                var bgR = colors.bgR, bgG = colors.bgG, bgB = colors.bgB
                var bgA: Float = 0

                if let cell, !cell.isEmpty {
                    if !cell.codepoints.isEmpty {
                        glyphIdx = fontAtlas.glyphIndex(
                            for: cell.codepoints, bold: cell.bold, italic: cell.italic)
                    }
                    if let fg = cell.fgColor {
                        fgR = Float(fg.r) / 255.0
                        fgG = Float(fg.g) / 255.0
                        fgB = Float(fg.b) / 255.0
                    }
                    if let bg = cell.bgColor {
                        bgR = Float(bg.r) / 255.0
                        bgG = Float(bg.g) / 255.0
                        bgB = Float(bg.b) / 255.0
                        bgA = 1.0
                        flags |= 0x10
                    }
                    if cell.bold { flags |= 0x01 }
                    if cell.italic { flags |= 0x02 }
                    if cell.underline { flags |= 0x04 }
                    if cell.strikethrough { flags |= 0x08 }
                }

                let linearIndex = rowIndex * cols + colIndex

                if let selectedCells, selectedCells.contains(linearIndex) {
                    bgR = 0.35; bgG = 0.45; bgB = 0.65; bgA = 1.0
                    fgR = 1.0; fgG = 1.0; fgB = 1.0
                    flags |= 0x10
                }

                if let currentMatchCells, currentMatchCells.contains(linearIndex) {
                    bgR = 0.98; bgG = 0.73; bgB = 0.18; bgA = 0.8
                    flags |= 0x10
                } else if let highlightedCells, highlightedCells.contains(linearIndex) {
                    bgR = 0.98; bgG = 0.73; bgB = 0.18; bgA = 0.4
                    flags |= 0x10
                }

                instances.append(CellInstance(
                    posX: x, posY: y,
                    fgR: fgR, fgG: fgG, fgB: fgB, fgA: 1.0,
                    bgR: bgR, bgG: bgG, bgB: bgB, bgA: bgA,
                    glyphIndex: glyphIdx, flags: flags
                ))
            }
        }

        instanceCount = instances.count
        if instanceCount > 0 {
            let byteCount = instanceCount * MemoryLayout<CellInstance>.stride
            if byteCount > instanceBufferCapacity {
                let allocSize = max(byteCount, instanceBufferCapacity * 2)
                instanceBuffer = device.makeBuffer(length: allocSize, options: .storageModeShared)
                instanceBufferCapacity = allocSize
            }
            instanceBuffer?.contents().copyMemory(from: instances, byteCount: byteCount)
        }

        if let cursor {
            let cursorY = vpHeight - Float(cursor.y + 1) * ch
            let cursorX = Float(cursor.x) * cw
            let style: UInt32 = cursor.style

            var cursorInst = CursorInstanceGPU(
                posX: cursorX, posY: cursorY,
                colorR: cursor.colorR, colorG: cursor.colorG, colorB: cursor.colorB, colorA: 1.0,
                cellWidth: cw, cellHeight: ch, style: style, padding: 0
            )
            let cursorByteCount = MemoryLayout<CursorInstanceGPU>.stride
            if cursorByteCount > cursorBufferCapacity {
                cursorBuffer = device.makeBuffer(length: cursorByteCount, options: .storageModeShared)
                cursorBufferCapacity = cursorByteCount
            }
            cursorBuffer?.contents().copyMemory(from: &cursorInst, byteCount: cursorByteCount)
            hasCursor = true
        } else {
            hasCursor = false
        }

        updateUniforms()
    }

    private struct CursorInstanceGPU {
        var posX: Float
        var posY: Float
        var colorR: Float
        var colorG: Float
        var colorB: Float
        var colorA: Float
        var cellWidth: Float
        var cellHeight: Float
        var style: UInt32
        var padding: UInt32
    }

    private func updateUniforms() {
        let scale = Float(backingScaleFactor)
        var cellUniforms = CellUniforms(
            viewportWidth: Float(viewportSize.width),
            viewportHeight: Float(viewportSize.height),
            cellWidth: Float(cellWidth) * scale,
            cellHeight: Float(cellHeight) * scale,
            atlasWidth: Float(fontAtlas.texture.width),
            atlasHeight: Float(fontAtlas.texture.height),
            glyphWidth: Float(fontAtlas.glyphWidth),
            glyphHeight: Float(fontAtlas.glyphHeight),
            glyphsPerRow: UInt32(fontAtlas.glyphsPerRow)
        )
        let cellByteCount = MemoryLayout<CellUniforms>.stride
        if cellUniformBuffer == nil {
            cellUniformBuffer = device.makeBuffer(length: cellByteCount, options: .storageModeShared)
        }
        cellUniformBuffer?.contents().copyMemory(from: &cellUniforms, byteCount: cellByteCount)

        var cursorUniforms = CursorUniformsGPU(
            viewportWidth: Float(viewportSize.width),
            viewportHeight: Float(viewportSize.height)
        )
        let cursorByteCount = MemoryLayout<CursorUniformsGPU>.stride
        if cursorUniformBuffer == nil {
            cursorUniformBuffer = device.makeBuffer(length: cursorByteCount, options: .storageModeShared)
        }
        cursorUniformBuffer?.contents().copyMemory(from: &cursorUniforms, byteCount: cursorByteCount)
    }

    private struct CursorUniformsGPU {
        var viewportWidth: Float
        var viewportHeight: Float
    }

    // MARK: - MTKViewDelegate

    public func mtkView(_ view: MTKView, drawableSizeWillChange size: CGSize) {
        viewportSize = size
        let pointSize = CGSize(
            width: size.width / backingScaleFactor,
            height: size.height / backingScaleFactor
        )
        let (cols, rows) = calculateGridSize(for: pointSize)
        gridCols = cols
        gridRows = rows
        updateUniforms()
    }

    public func draw(in view: MTKView) {
        guard let drawable = view.currentDrawable,
              let renderPassDescriptor = view.currentRenderPassDescriptor else {
            return
        }

        renderPassDescriptor.colorAttachments[0].clearColor = MTLClearColor(
            red: Double(defaultBG.0), green: Double(defaultBG.1), blue: Double(defaultBG.2), alpha: 1.0)
        renderPassDescriptor.colorAttachments[0].loadAction = .clear
        renderPassDescriptor.colorAttachments[0].storeAction = .store

        guard let commandBuffer = commandQueue.makeCommandBuffer(),
              let encoder = commandBuffer.makeRenderCommandEncoder(descriptor: renderPassDescriptor) else {
            return
        }

        if instanceCount > 0, let instanceBuffer, let cellUniformBuffer {
            encoder.setRenderPipelineState(cellPipelineState)
            encoder.setVertexBuffer(instanceBuffer, offset: 0, index: 0)
            encoder.setVertexBuffer(cellUniformBuffer, offset: 0, index: 1)
            encoder.setFragmentBuffer(cellUniformBuffer, offset: 0, index: 1)
            encoder.setFragmentTexture(fontAtlas.texture, index: 0)
            encoder.drawPrimitives(type: .triangle, vertexStart: 0, vertexCount: 6, instanceCount: instanceCount)
        }

        if hasCursor, let cursorBuffer, let cursorUniformBuffer {
            encoder.setRenderPipelineState(cursorPipelineState)
            encoder.setVertexBuffer(cursorBuffer, offset: 0, index: 0)
            encoder.setVertexBuffer(cursorUniformBuffer, offset: 0, index: 1)
            encoder.drawPrimitives(type: .triangle, vertexStart: 0, vertexCount: 6, instanceCount: 1)
        }

        encoder.endEncoding()
        commandBuffer.present(drawable)
        commandBuffer.commit()
    }
}

// MARK: - Convenience: building from ghostty render state

public extension MetalTerminalRenderer.TerminalColors {
    init(from colors: GhosttyRenderStateColors) {
        self.init(
            fgR: Float(colors.foreground.r) / 255.0,
            fgG: Float(colors.foreground.g) / 255.0,
            fgB: Float(colors.foreground.b) / 255.0,
            bgR: Float(colors.background.r) / 255.0,
            bgG: Float(colors.background.g) / 255.0,
            bgB: Float(colors.background.b) / 255.0,
            cursorR: Float(colors.cursor.r) / 255.0,
            cursorG: Float(colors.cursor.g) / 255.0,
            cursorB: Float(colors.cursor.b) / 255.0,
            cursorHasValue: colors.cursor_has_value
        )
    }
}

public extension MetalTerminalRenderer.CursorInfo {
    /// Map ghostty cursor style constants to the shader's style indices.
    /// Ghostty: BAR=0, BLOCK=1, UNDERLINE=2, BLOCK_HOLLOW=3.
    /// Shader:  BLOCK=0, BAR=1, UNDERLINE=2, HOLLOW=3.
    init(x: Int, y: Int, ghosttyStyle: UInt32, colors: MetalTerminalRenderer.TerminalColors) {
        self.x = x
        self.y = y

        let style: UInt32
        switch ghosttyStyle {
        case 0: style = 1
        case 1: style = 0
        default: style = ghosttyStyle
        }
        self.style = style

        if colors.cursorHasValue {
            self.colorR = colors.cursorR
            self.colorG = colors.cursorG
            self.colorB = colors.cursorB
        } else {
            self.colorR = 0.96
            self.colorG = 0.88
            self.colorB = 0.86
        }
    }
}
