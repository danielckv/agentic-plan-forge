import express from "express";
import { helper } from "./helper.js";

export function add(a, b) {
  return a + b;
}

function internal(x) {
  return x;
}

export class Counter {
  increment(by) {
    return by;
  }
}

export const double = (n) => n * 2;

const app = express();
app.get("/users", (req, res) => res.json([]));
