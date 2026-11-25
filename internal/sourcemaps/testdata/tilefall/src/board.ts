// Tilefall's board: the made-up game the docs use.
export interface Tile {
  value: number;
  label?: string;
}

export class Board {
  static readonly banner = "Tilefall ✨ — drop the tiles 🧩";
  cells: (Tile | undefined)[] = [];

  constructor(private size: number) {}

  weight(tile: Tile | undefined): number {
    return tile!.value * 2;
  }

  score(tiles: (Tile | undefined)[]): number {
    let total = 0;
    for (const t of tiles) total += this.weight(t);
    return total;
  }

  get label(): string {
    return `${Board.banner} ${this.cells.length}/${this.size}`.replace(/\s+/g, " ");
  }

  static parse(text: string): Board {
    const sizes = text.split(",").map((part) => {
      const n = Number(part);
      if (!Number.isFinite(n)) throw new RangeError(`not a size: ${part}`);
      return n;
    });
    return new Board(sizes[0]);
  }

  labels(): string[] {
    return this.cells.map((tile) => tile!.label!.toUpperCase());
  }
}

export function dropTile(board: Board, tiles: (Tile | undefined)[]) {
  const result = board.score(tiles);
  return result + 1;
}
