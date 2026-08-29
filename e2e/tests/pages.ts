// The gallery's page list, derived rather than typed out.
//
// Two specs need to know what the gallery should contain: demo-shots.spec.ts
// captures one screenshot per page, and gallery-journeys.spec.ts asserts the
// sidebar and the home grid render the same list. Both used to carry their own
// hand-written copy, and both fell behind the same way — a hardcoded
// `toHaveCount(17)` failed the moment relayboard was listed, and the recipe
// array had been stuck at two entries since before relayboard existed.
//
// plugins.json is the repo's own answer to "what ships", and the directories
// under recipes/ are the answer for recipes. Reading both is what makes adding
// a plugin or a recipe a one-place change. Go's
// TestGalleryListsEveryShippedPlugin and TestGalleryListsEveryShippedRecipe
// enforce the same two sources on the Go side.
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

const repoRoot = join(__dirname, "..", "..");

export const PLUGIN_SLUGS: string[] = (
  JSON.parse(readFileSync(join(repoRoot, "plugins.json"), "utf8")) as {
    plugins: { name: string }[];
  }
).plugins.map((p) => p.name);

export const RECIPES: string[] = readdirSync(join(repoRoot, "recipes"), { withFileTypes: true })
  .filter((d) => d.isDirectory())
  .map((d) => d.name)
  .sort();

/** Every slug the gallery links, plugins first then recipes. */
export const PAGES = [...PLUGIN_SLUGS, ...RECIPES];
