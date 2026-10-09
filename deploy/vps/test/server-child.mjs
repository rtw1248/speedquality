import { startPublicServer } from "../server.mjs";
let server;
process.on("message", async (message) => {
  try {
    if (message === "stop") { await server.stop(); process.disconnect(); return; }
    server = await startPublicServer(message, { resolveGeo: async () => ({}) });
    process.send({ port: server.address.port });
  } catch (error) { process.send({ error: error.message }); }
});
