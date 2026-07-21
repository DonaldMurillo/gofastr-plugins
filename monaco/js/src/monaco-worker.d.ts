// The basic-languages contribution modules are plain side-effect imports
// (they register monarch tokenizers on the monaco namespace at load time and
// export nothing the bundle consumes). Declared as empty modules so the
// type-checker accepts the side-effect imports.
declare module "monaco-editor/esm/vs/basic-languages/*" {}

declare module "monaco-editor/esm/vs/editor/editor.worker.js" {
  // The worker entry source, inlined as a string by build.mjs (onLoad plugin).
  // Used only when the host opts into workers; the default worker-free path
  // never constructs a real Worker.
  const editorWorkerSource: string;
  export default editorWorkerSource;
}
