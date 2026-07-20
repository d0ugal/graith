const MIN_ZOOM = 1;
const MAX_ZOOM = 12;
const BUTTON_ZOOM_FACTOR = 1.25;

export function parseViewBox(value) {
    if (typeof value !== "string") {
        return null;
    }

    const parts = value.trim().split(/[\s,]+/).map(Number);
    if (parts.length !== 4 || parts.some((part) => !Number.isFinite(part))) {
        return null;
    }

    const [x, y, width, height] = parts;
    if (width <= 0 || height <= 0) {
        return null;
    }

    return { x, y, width, height };
}

export function constrainViewBox(viewBox, bounds) {
    const width = Math.min(Math.max(viewBox.width, bounds.width / MAX_ZOOM), bounds.width);
    const height = Math.min(Math.max(viewBox.height, bounds.height / MAX_ZOOM), bounds.height);
    const maxX = bounds.x + bounds.width - width;
    const maxY = bounds.y + bounds.height - height;

    return {
        x: Math.min(Math.max(viewBox.x, bounds.x), maxX),
        y: Math.min(Math.max(viewBox.y, bounds.y), maxY),
        width,
        height,
    };
}

export function zoomViewBox(viewBox, bounds, point, factor) {
    const currentZoom = bounds.width / viewBox.width;
    const targetZoom = Math.min(Math.max(currentZoom * factor, MIN_ZOOM), MAX_ZOOM);
    const width = bounds.width / targetZoom;
    const height = bounds.height / targetZoom;
    const anchorX = (point.x - viewBox.x) / viewBox.width;
    const anchorY = (point.y - viewBox.y) / viewBox.height;

    return constrainViewBox({
        x: point.x - anchorX * width,
        y: point.y - anchorY * height,
        width,
        height,
    }, bounds);
}

export function panViewBox(viewBox, bounds, delta) {
    return constrainViewBox({
        ...viewBox,
        x: viewBox.x - delta.x,
        y: viewBox.y - delta.y,
    }, bounds);
}

export function zoomPercentage(viewBox, bounds) {
    return Math.round((bounds.width / viewBox.width) * 100);
}

function copyViewBox(viewBox) {
    return { ...viewBox };
}

function formatViewBox(viewBox) {
    return `${viewBox.x} ${viewBox.y} ${viewBox.width} ${viewBox.height}`;
}

function pointerGesture(pointers) {
    const points = [...pointers.values()];
    if (points.length === 0) {
        return null;
    }
    if (points.length === 1) {
        return { center: points[0], distance: 0 };
    }

    const [first, second] = points;
    return {
        center: {
            x: (first.x + second.x) / 2,
            y: (first.y + second.y) / 2,
        },
        distance: Math.hypot(second.x - first.x, second.y - first.y),
    };
}

function clientPointToSVG(svg, point) {
    const matrix = svg.getScreenCTM();
    if (!matrix) {
        return null;
    }

    const svgPoint = svg.createSVGPoint();
    svgPoint.x = point.x;
    svgPoint.y = point.y;
    return svgPoint.matrixTransform(matrix.inverse());
}

function graphSVG(root) {
    return root.querySelector("[data-package-graph-viewport] svg");
}

export function initialisePackageGraph(root) {
    if (root.dataset.packageGraphReady === "true") {
        return true;
    }

    const viewport = root.querySelector("[data-package-graph-viewport]");
    const svg = graphSVG(root);
    if (!viewport || !svg) {
        return false;
    }

    const initialViewBox = parseViewBox(svg.getAttribute("viewBox"));
    if (!initialViewBox) {
        return false;
    }

    const bounds = copyViewBox(initialViewBox);
    let viewBox = copyViewBox(initialViewBox);
    const pointers = new Map();
    const zoomOutput = root.querySelector("[data-package-graph-zoom]");
    const zoomInButton = root.querySelector("[data-package-graph-zoom-in]");
    const zoomOutButton = root.querySelector("[data-package-graph-zoom-out]");
    const resetButton = root.querySelector("[data-package-graph-reset]");
    const fullscreenButton = root.querySelector("[data-package-graph-fullscreen]");
    const fullscreenIcon = fullscreenButton?.querySelector(".material-icons");
    const ownerDocument = root.ownerDocument;

    svg.setAttribute("preserveAspectRatio", "xMidYMid meet");

    function applyViewBox(nextViewBox, announce = true) {
        viewBox = constrainViewBox(nextViewBox, bounds);
        svg.setAttribute("viewBox", formatViewBox(viewBox));

        const percentage = zoomPercentage(viewBox, bounds);
        if (zoomOutput) {
            zoomOutput.value = `${percentage}%`;
            zoomOutput.textContent = `${percentage}%`;
        }
        if (zoomInButton) {
            zoomInButton.disabled = percentage >= MAX_ZOOM * 100;
        }
        if (zoomOutButton) {
            zoomOutButton.disabled = percentage <= MIN_ZOOM * 100;
        }
        if (announce) {
            root.setAttribute("aria-label", `Package dependency graph, zoom ${percentage}%`);
        }
    }

    function zoomAtClientPoint(point, factor) {
        const svgPoint = clientPointToSVG(svg, point);
        if (svgPoint) {
            applyViewBox(zoomViewBox(viewBox, bounds, svgPoint, factor));
        }
    }

    function zoomAtCenter(factor) {
        zoomAtClientPoint({
            x: viewport.getBoundingClientRect().left + viewport.clientWidth / 2,
            y: viewport.getBoundingClientRect().top + viewport.clientHeight / 2,
        }, factor);
    }

    function reset() {
        applyViewBox(copyViewBox(bounds));
    }

    zoomInButton?.addEventListener("click", () => zoomAtCenter(BUTTON_ZOOM_FACTOR));
    zoomOutButton?.addEventListener("click", () => zoomAtCenter(1 / BUTTON_ZOOM_FACTOR));
    resetButton?.addEventListener("click", reset);

    viewport.addEventListener("wheel", (event) => {
        event.preventDefault();
        zoomAtClientPoint({ x: event.clientX, y: event.clientY }, Math.exp(-event.deltaY * 0.002));
    }, { passive: false });

    viewport.addEventListener("pointerdown", (event) => {
        if (event.pointerType === "mouse" && event.button !== 0) {
            return;
        }

        event.preventDefault();
        viewport.setPointerCapture(event.pointerId);
        pointers.set(event.pointerId, { x: event.clientX, y: event.clientY });
        viewport.classList.add("is-panning");
    });

    viewport.addEventListener("pointermove", (event) => {
        if (!pointers.has(event.pointerId)) {
            return;
        }

        event.preventDefault();
        const previousGesture = pointerGesture(pointers);
        pointers.set(event.pointerId, { x: event.clientX, y: event.clientY });
        const nextGesture = pointerGesture(pointers);
        if (!previousGesture || !nextGesture) {
            return;
        }

        const previousPoint = clientPointToSVG(svg, previousGesture.center);
        const nextPoint = clientPointToSVG(svg, nextGesture.center);
        if (previousPoint && nextPoint) {
            applyViewBox(panViewBox(viewBox, bounds, {
                x: nextPoint.x - previousPoint.x,
                y: nextPoint.y - previousPoint.y,
            }), false);
        }

        if (previousGesture.distance > 0 && nextGesture.distance > 0) {
            zoomAtClientPoint(nextGesture.center, nextGesture.distance / previousGesture.distance);
        }
    });

    function releasePointer(event) {
        pointers.delete(event.pointerId);
        if (pointers.size === 0) {
            viewport.classList.remove("is-panning");
        }
    }

    viewport.addEventListener("pointerup", releasePointer);
    viewport.addEventListener("pointercancel", releasePointer);
    viewport.addEventListener("lostpointercapture", releasePointer);

    viewport.addEventListener("keydown", (event) => {
        const key = event.key;
        if (["+", "=", "-", "_", "0", "ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"].includes(key)) {
            event.preventDefault();
        }

        switch (key) {
        case "+":
        case "=":
            zoomAtCenter(BUTTON_ZOOM_FACTOR);
            break;
        case "-":
        case "_":
            zoomAtCenter(1 / BUTTON_ZOOM_FACTOR);
            break;
        case "0":
            reset();
            break;
        case "ArrowLeft":
            applyViewBox(constrainViewBox({ ...viewBox, x: viewBox.x - viewBox.width * 0.1 }, bounds));
            break;
        case "ArrowRight":
            applyViewBox(constrainViewBox({ ...viewBox, x: viewBox.x + viewBox.width * 0.1 }, bounds));
            break;
        case "ArrowUp":
            applyViewBox(constrainViewBox({ ...viewBox, y: viewBox.y - viewBox.height * 0.1 }, bounds));
            break;
        case "ArrowDown":
            applyViewBox(constrainViewBox({ ...viewBox, y: viewBox.y + viewBox.height * 0.1 }, bounds));
            break;
        }
    });

    if (!root.requestFullscreen || !ownerDocument.exitFullscreen) {
        if (fullscreenButton) {
            fullscreenButton.hidden = true;
        }
    } else {
        fullscreenButton?.addEventListener("click", async () => {
            try {
                if (ownerDocument.fullscreenElement === root) {
                    await ownerDocument.exitFullscreen();
                } else {
                    await root.requestFullscreen();
                }
            } catch {
                root.setAttribute("aria-label", "Package dependency graph; full screen is unavailable");
            }
        });

        ownerDocument.addEventListener("fullscreenchange", () => {
            const active = ownerDocument.fullscreenElement === root;
            fullscreenButton?.setAttribute("aria-label", active ? "Exit full screen" : "View full screen");
            fullscreenButton?.setAttribute("title", active ? "Exit full screen" : "View full screen");
            if (fullscreenIcon) {
                fullscreenIcon.textContent = active ? "fullscreen_exit" : "fullscreen";
            }
        });
    }

    root.dataset.packageGraphReady = "true";
    applyViewBox(viewBox, false);
    return true;
}

export function initialisePackageGraphs(documentRoot = document) {
    documentRoot.querySelectorAll("[data-package-graph]").forEach((root) => {
        if (initialisePackageGraph(root)) {
            return;
        }

        const observer = new MutationObserver(() => {
            if (initialisePackageGraph(root)) {
                observer.disconnect();
            }
        });
        observer.observe(root, { childList: true, subtree: true });
    });
}

if (typeof document !== "undefined") {
    if (document.readyState === "loading") {
        document.addEventListener("DOMContentLoaded", () => initialisePackageGraphs(), { once: true });
    } else {
        initialisePackageGraphs();
    }
}
