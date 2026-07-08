#if canImport(UIKit)
import UIKit
import GraithClientAPI

// Maps UIKit hardware-keyboard events to the logical `TerminalKey` /
// `TerminalModifiers` the VT core encodes. iOS hardware-key input arrives via
// `UIKey` (with a `UIKeyboardHIDUsage`), which is a different world from macOS
// virtual key codes — hence a rewrite, not a port (design §C.3).

enum UIKeyMapping {
    /// Translate a `UIKey` into a logical stroke, or nil if it's a plain
    /// character better handled through text input (so IME/layout is respected).
    static func stroke(for key: UIKey, action: TerminalKeyAction) -> TerminalKeyStroke? {
        let mods = modifiers(from: key.modifierFlags)

        if let special = specialKey(for: key.keyCode) {
            return TerminalKeyStroke(key: special, modifiers: mods, action: action)
        }

        // A control/alt/command chord over a character must go through the
        // encoder (e.g. Ctrl-C), not insertText — carry the character text.
        let chars = key.charactersIgnoringModifiers
        if !chars.isEmpty, mods.contains(.control) || mods.contains(.option) || mods.contains(.command) {
            return TerminalKeyStroke(key: .character(chars), modifiers: mods, action: action)
        }

        // Otherwise let UIKeyInput/UITextInput deliver the (possibly IME-composed)
        // text; return nil so pressesBegan doesn't double-handle it.
        return nil
    }

    static func modifiers(from flags: UIKeyModifierFlags) -> TerminalModifiers {
        var mods: TerminalModifiers = []
        if flags.contains(.shift) { mods.insert(.shift) }
        if flags.contains(.control) { mods.insert(.control) }
        if flags.contains(.alternate) { mods.insert(.option) }
        if flags.contains(.command) { mods.insert(.command) }
        return mods
    }

    /// Non-printable keys the terminal needs as escape sequences.
    static func specialKey(for code: UIKeyboardHIDUsage) -> TerminalKey? {
        switch code {
        case .keyboardReturnOrEnter, .keypadEnter: return .enter
        case .keyboardTab: return .tab
        case .keyboardDeleteOrBackspace: return .backspace
        case .keyboardEscape: return .escape
        case .keyboardDeleteForward: return .delete
        case .keyboardUpArrow: return .arrowUp
        case .keyboardDownArrow: return .arrowDown
        case .keyboardLeftArrow: return .arrowLeft
        case .keyboardRightArrow: return .arrowRight
        case .keyboardHome: return .home
        case .keyboardEnd: return .end
        case .keyboardPageUp: return .pageUp
        case .keyboardPageDown: return .pageDown
        case .keyboardInsert: return .insert
        case .keyboardF1: return .function(1)
        case .keyboardF2: return .function(2)
        case .keyboardF3: return .function(3)
        case .keyboardF4: return .function(4)
        case .keyboardF5: return .function(5)
        case .keyboardF6: return .function(6)
        case .keyboardF7: return .function(7)
        case .keyboardF8: return .function(8)
        case .keyboardF9: return .function(9)
        case .keyboardF10: return .function(10)
        case .keyboardF11: return .function(11)
        case .keyboardF12: return .function(12)
        default: return nil
        }
    }
}
#endif
