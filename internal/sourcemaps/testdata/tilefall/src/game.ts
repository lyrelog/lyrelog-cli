import { Board, dropTile, type Tile } from "./board";

export const rules = {
  check(board: Board): void {
    if (board.cells.length > 2) throw new Error("too many cells 🎲");
  },
  onTap: (n: number) => {
    const board = new Board(n);
    return dropTile(board, [{ value: 1 }, undefined]);
  },
};

export async function loadLevel(name: string): Promise<Board> {
  await Promise.resolve();
  const ratio = 4 / 2; // a division, not a regular expression
  return Board.parse(name + "," + ratio);
}

export function play(kind: string): unknown {
  const tiles: Tile[] = [{ value: 1 }];
  function inner() {
    switch (kind) {
      case "tap":
        return rules.onTap(4);
      case "parse":
        return Board.parse("x");
      case "labels": {
        const b = new Board(2);
        b.cells = [{ value: 1 }];
        return b.labels();
      }
      case "check": {
        const b = new Board(2);
        b.cells = [...tiles, ...tiles, ...tiles];
        return rules.check(b);
      }
    }
    return undefined;
  }
  return inner();
}
