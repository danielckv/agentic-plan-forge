import { Request } from "express";

export interface Options {
  retries: number;
}

export type Handler = (req: Request) => void;

export enum Mode {
  Fast,
  Slow,
}

export function connect(url: string, options?: Options): Promise<void> {
  return Promise.resolve();
}

export class Client {
  send(data: string): void {
    console.log(data);
  }
}
