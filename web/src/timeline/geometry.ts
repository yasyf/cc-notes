// Vertical geometry for the swimlane body: fixed pixel constants and the
// per-row metrics that turn a LayoutResult's packed rows and sub-rows into y
// coordinates. Pure arithmetic, shared by the renderer's pieces.

import type { LayoutResult } from "./layout";

export const AXIS_HEIGHT = 30;
export const LABEL_STRIP = 18; // room above a row's rails for inline labels
export const GUTTER_WIDTH = 176; // fixed left column holding the lane labels
export const SUBROW_H = 22;
export const LANE_PAD = 8;
export const MARKER_SIZE = 11; // >= 8px per the dataviz mark spec
export const SPAN_HEIGHT = 12; // <= 24px bar thickness
export const RAIL_STROKE = 2;
export const CONNECTOR_RADIUS = 22;

export interface RowMetrics {
  rowTop: number[];
  rowHeight: number[];
  totalHeight: number;
  railY: (row: number) => number;
  itemY: (row: number, subRow: number) => number;
  labelY: (row: number) => number;
}

// rowMetrics computes each row's height from the tallest lane on it (rows are
// shared by non-overlapping siblings) and the cumulative y offsets.
export function rowMetrics(result: LayoutResult): RowMetrics {
  const subRows = new Array<number>(result.rowCount).fill(1);
  for (const lane of result.lanes) {
    if (lane.row < subRows.length) {
      subRows[lane.row] = Math.max(subRows[lane.row] ?? 1, lane.subRows);
    }
  }
  const rowHeight = subRows.map((n) => LABEL_STRIP + n * SUBROW_H + LANE_PAD);
  const rowTop: number[] = [];
  let y = 0;
  for (const h of rowHeight) {
    rowTop.push(y);
    y += h;
  }
  const top = (row: number) => rowTop[row] ?? 0;
  return {
    rowTop,
    rowHeight,
    totalHeight: y,
    railY: (row) => top(row) + LABEL_STRIP + SUBROW_H / 2,
    itemY: (row, subRow) => top(row) + LABEL_STRIP + subRow * SUBROW_H + SUBROW_H / 2,
    labelY: (row) => top(row) + LABEL_STRIP - 5,
  };
}
