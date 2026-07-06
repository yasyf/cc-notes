// d3 math for the timeline x axis: a scaleTime over the layout domain, an
// x-only zoom transform, and tick formatting. d3 is used for arithmetic only —
// React owns every DOM node, so nothing here selects or mutates the DOM.

import { scaleTime, type ScaleTime } from "d3-scale";
import { zoomIdentity, type ZoomTransform } from "d3-zoom";

export type TimeScale = ScaleTime<number, number>;

// baseScale maps the layout domain (unix seconds) to pixels [0, width].
export function baseScale(domain: [number, number], width: number): TimeScale {
  return scaleTime()
    .domain([new Date(domain[0] * 1000), new Date(domain[1] * 1000)])
    .range([0, Math.max(1, width)]);
}

// zoomTransform builds an x-only ZoomTransform from a scale factor and pixel
// offset, so callers keep zoom state as two plain numbers.
export function zoomTransform(k: number, x: number): ZoomTransform {
  return zoomIdentity.translate(x, 0).scale(k);
}

// displayScale applies the current zoom to the base scale, yielding the scale
// the axis and marks are drawn against.
export function displayScale(base: TimeScale, k: number, x: number): TimeScale {
  return zoomTransform(k, x).rescaleX(base);
}

export interface ZoomState {
  k: number;
  x: number;
}

export const IDENTITY_ZOOM: ZoomState = { k: 1, x: 0 };

export interface ZoomLimits {
  minK: number;
  maxK: number;
  width: number;
}

// clampZoom keeps k within [minK, maxK] and pins the pan so the content always
// covers the viewport — no dead space past either edge of the domain.
export function clampZoom(state: ZoomState, limits: ZoomLimits): ZoomState {
  const k = Math.min(limits.maxK, Math.max(limits.minK, state.k));
  const minX = limits.width * (1 - k);
  const x = Math.min(0, Math.max(minX, state.x));
  return { k, x };
}

// zoomAt rescales around a fixed pixel px (keeping the time under the cursor put)
// by multiplying k by factor, then re-clamps.
export function zoomAt(
  state: ZoomState,
  px: number,
  factor: number,
  limits: ZoomLimits,
): ZoomState {
  const k = Math.min(limits.maxK, Math.max(limits.minK, state.k * factor));
  const x = px - ((px - state.x) / state.k) * k;
  return clampZoom({ k, x }, limits);
}

// panBy shifts the view horizontally by dx pixels, re-clamped to the domain.
export function panBy(state: ZoomState, dx: number, limits: ZoomLimits): ZoomState {
  return clampZoom({ k: state.k, x: state.x + dx }, limits);
}

export interface Tick {
  value: Date;
  label: string;
  x: number;
}

// ticks returns the axis ticks for the current display scale, positioned in
// pixels with d3's multi-scale time format.
export function ticks(scale: TimeScale, count: number): Tick[] {
  const values = scale.ticks(count);
  const format = scale.tickFormat(count);
  return values.map((value) => ({ value, label: format(value), x: scale(value) }));
}
