import { loadConfig } from "./config.mjs";
import { JsonStore } from "./store.mjs";
import { DolaAdapterService } from "./service.mjs";
import { DolaHTTPServer } from "./http-server.mjs";

const config = loadConfig();
const store = new JsonStore(config.stateFile);
await store.load();
const service = new DolaAdapterService({ store, config });
const server = new DolaHTTPServer(service, config);

await service.start();
await server.listen();
console.log(`Dola New API Adapter listening on ${config.host}:${config.port}`);

let stopping = false;
async function stop(signal) {
    if (stopping) return;
    stopping = true;
    console.log(`received ${signal}, stopping Dola Adapter`);
    await server.close().catch(() => undefined);
    await service.stop().catch(() => undefined);
    process.exitCode = 0;
}

process.on("SIGINT", () => void stop("SIGINT"));
process.on("SIGTERM", () => void stop("SIGTERM"));
