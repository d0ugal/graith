import assert from "node:assert/strict";
import test from "node:test";

import {
    constrainViewBox,
    initialisePackageGraph,
    panViewBox,
    parseViewBox,
    zoomPercentage,
    zoomViewBox,
} from "../assets/js/package-graph.mjs";

const bounds = { x: 10, y: 20, width: 1000, height: 500 };

class FakeElement extends EventTarget {
    constructor() {
        super();
        this.attributes = new Map();
        this.dataset = {};
        this.disabled = false;
        this.hidden = false;
        this.textContent = "";
        this.value = "";
    }

    getAttribute(name) {
        return this.attributes.get(name) ?? null;
    }

    setAttribute(name, value) {
        this.attributes.set(name, String(value));
    }
}

function fakeGraphElements() {
    const ownerDocument = new FakeElement();
    ownerDocument.fullscreenElement = null;

    const svg = new FakeElement();
    svg.setAttribute("viewBox", "10 20 1000 500");
    svg.getScreenCTM = () => ({ inverse: () => ({}) });
    svg.createSVGPoint = () => ({
        x: 0,
        y: 0,
        matrixTransform() {
            return { x: this.x + 10, y: this.y + 20 };
        },
    });

    const viewport = new FakeElement();
    viewport.clientWidth = 1000;
    viewport.clientHeight = 500;
    viewport.getBoundingClientRect = () => ({ left: 0, top: 0 });
    viewport.setPointerCapture = () => {};
    const classes = new Set();
    viewport.classList = {
        add: (name) => classes.add(name),
        remove: (name) => classes.delete(name),
    };

    const zoomIn = new FakeElement();
    const zoomOut = new FakeElement();
    const reset = new FakeElement();
    const fullscreen = new FakeElement();
    const fullscreenIcon = new FakeElement();
    fullscreen.querySelector = () => fullscreenIcon;
    const zoomOutput = new FakeElement();

    const elements = new Map([
        ["[data-package-graph-viewport]", viewport],
        ["[data-package-graph-viewport] svg", svg],
        ["[data-package-graph-zoom]", zoomOutput],
        ["[data-package-graph-zoom-in]", zoomIn],
        ["[data-package-graph-zoom-out]", zoomOut],
        ["[data-package-graph-reset]", reset],
        ["[data-package-graph-fullscreen]", fullscreen],
    ]);

    const root = new FakeElement();
    root.ownerDocument = ownerDocument;
    root.requestFullscreen = async () => {
        ownerDocument.fullscreenElement = root;
        ownerDocument.dispatchEvent(new Event("fullscreenchange"));
    };
    ownerDocument.exitFullscreen = async () => {
        ownerDocument.fullscreenElement = null;
        ownerDocument.dispatchEvent(new Event("fullscreenchange"));
    };
    root.querySelector = (selector) => elements.get(selector) ?? null;

    return { root, svg, zoomIn, zoomOut, reset, fullscreen, fullscreenIcon, zoomOutput, ownerDocument };
}

test("parseViewBox accepts spaces and commas", () => {
    assert.deepEqual(parseViewBox("10, 20 1000 500"), bounds);
});

test("parseViewBox rejects malformed or empty boxes", () => {
    assert.equal(parseViewBox("10 20 30"), null);
    assert.equal(parseViewBox("10 20 0 500"), null);
    assert.equal(parseViewBox(null), null);
});

test("zoomViewBox keeps the selected point stationary", () => {
    const got = zoomViewBox(bounds, bounds, { x: 260, y: 145 }, 2);
    assert.deepEqual(got, { x: 135, y: 82.5, width: 500, height: 250 });
    assert.equal(zoomPercentage(got, bounds), 200);
});

test("zoomViewBox clamps to the supported range", () => {
    const maximum = zoomViewBox(bounds, bounds, { x: 510, y: 270 }, 100);
    assert.equal(zoomPercentage(maximum, bounds), 1200);

    const reset = zoomViewBox(maximum, bounds, { x: 510, y: 270 }, 0.001);
    assert.deepEqual(reset, bounds);
});

test("panViewBox cannot lose the graph beyond its bounds", () => {
    const zoomed = { x: 260, y: 145, width: 500, height: 250 };
    assert.deepEqual(
        panViewBox(zoomed, bounds, { x: 5000, y: 5000 }),
        { x: 10, y: 20, width: 500, height: 250 },
    );
    assert.deepEqual(
        panViewBox(zoomed, bounds, { x: -5000, y: -5000 }),
        { x: 510, y: 270, width: 500, height: 250 },
    );
});

test("constrainViewBox preserves the graph aspect ratio at maximum zoom", () => {
    assert.deepEqual(
        constrainViewBox({ x: 500, y: 250, width: 1, height: 1 }, bounds),
        { x: 500, y: 250, width: 1000 / 12, height: 500 / 12 },
    );
});

test("initialisePackageGraph wires zoom, reset, and full-screen controls", async () => {
    const {
        root,
        svg,
        zoomIn,
        zoomOut,
        reset,
        fullscreen,
        fullscreenIcon,
        zoomOutput,
        ownerDocument,
    } = fakeGraphElements();

    assert.equal(initialisePackageGraph(root), true);
    assert.equal(root.dataset.packageGraphReady, "true");
    assert.equal(zoomOutput.value, "100%");
    assert.equal(zoomOut.disabled, true);

    zoomIn.dispatchEvent(new Event("click"));
    assert.equal(svg.getAttribute("viewBox"), "110 70 800 400");
    assert.equal(zoomOutput.value, "125%");
    assert.equal(zoomOut.disabled, false);

    reset.dispatchEvent(new Event("click"));
    assert.equal(svg.getAttribute("viewBox"), "10 20 1000 500");
    assert.equal(zoomOutput.value, "100%");

    fullscreen.dispatchEvent(new Event("click"));
    await Promise.resolve();
    assert.equal(ownerDocument.fullscreenElement, root);
    assert.equal(fullscreen.getAttribute("aria-label"), "Exit full screen");
    assert.equal(fullscreenIcon.textContent, "fullscreen_exit");
});
