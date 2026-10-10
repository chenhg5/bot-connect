// bot-connect tools for a pi brain. Loaded with `pi -e`; registers exactly the
// tools this turn's caller may use (fetched from the turn's tool server), so a
// pi brain started with `--tools <these names>` has no shell and no file access.
import type { ExtensionAPI } from "@mariozechner/pi-coding-agent";
import { Type } from "typebox";

type BotTool = { name: string; description: string; input_schema: Record<string, unknown> };

export default async function (pi: ExtensionAPI) {
  const api = process.env.BOT_CONNECT_API;
  if (!api) return;
  const tools = (await (await fetch(api + "/tools")).json()) as BotTool[];
  for (const t of tools ?? []) {
    pi.registerTool({
      name: t.name,
      label: t.name,
      description: t.description,
      parameters: Type.Unsafe(t.input_schema),
      async execute(_id: string, params: unknown) {
        const res = await fetch(`${api}/tools/${t.name}`, {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify(params ?? {}),
        });
        const r = (await res.json()) as { ok: boolean; result?: string; error?: string };
        return {
          content: [{ type: "text" as const, text: r.ok ? r.result ?? "" : `error: ${r.error}` }],
          details: {},
          isError: !r.ok,
        };
      },
    });
  }
}
