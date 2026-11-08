/// <reference path="../pb_data/types.d.ts" />
// The bench collection: 1,000 rows of filler, public reads, writes only with
// the X-Bench-Key header (BENCH_KEY in the container's env).
migrate((app) => {
  const key = $os.getenv("BENCH_KEY").replace(/[^A-Za-z0-9]/g, "");
  const items = new Collection({
    type: "base",
    name: "items",
    listRule: "",
    viewRule: "",
    createRule: `@request.headers.x_bench_key = "${key}"`,
    fields: [
      { name: "title", type: "text" },
      { name: "body", type: "text" },
      { name: "n", type: "number" },
    ],
    indexes: ["CREATE INDEX idx_items_n ON items (n)"],
  });
  app.save(items);
  const filler = "Lorem ipsum dolor sit amet, consectetur adipiscing elit. ".repeat(6);
  for (let i = 0; i < 1000; i++) {
    const r = new Record(items);
    r.set("title", `item ${i}`);
    r.set("body", filler);
    r.set("n", i);
    app.save(r);
  }
}, (app) => {
  app.delete(app.findCollectionByNameOrId("items"));
});
