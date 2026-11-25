import { loadLevel, play } from "./game";

const report = (e: unknown) => console.log(JSON.stringify({ stack: (e as Error).stack }));

for (const kind of ["tap", "parse", "labels", "check"]) {
  try {
    play(kind);
  } catch (e) {
    report(e);
  }
}
loadLevel("level-✨").catch(report);
