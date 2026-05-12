import { Hono } from 'hono';
import { streamSSE } from 'hono/streaming';

const clients = new Set<{ push: (msg: string) => void; done: () => void }>();

setInterval(() => {
  const msg = `tick ${Math.floor(Date.now() / 1000)}`;
  let sent = 0;
  for (const c of clients) {
    c.push(msg);
    sent++;
  }
  console.log(`clients=${clients.size} sent=${sent}`);
}, 1000);

const app = new Hono();

app.get('/stream', (c) =>
  streamSSE(c, async (stream) => {
    let resolveNext: ((m: string) => void) | null = null;
    const queue: string[] = [];
    const handle = {
      push: (m: string) => {
        if (resolveNext) {
          const r = resolveNext;
          resolveNext = null;
          r(m);
        } else {
          queue.push(m);
        }
      },
      done: () => {
        if (resolveNext) resolveNext('__close__');
      },
    };
    clients.add(handle);

    await stream.writeSSE({ event: 'hello', data: 'ok' });

    stream.onAbort(() => {
      clients.delete(handle);
      handle.done();
    });

    try {
      while (true) {
        const msg =
          queue.shift() ??
          (await new Promise<string>((res) => (resolveNext = res)));
        if (msg === '__close__') break;
        await stream.writeSSE({ data: msg });
      }
    } finally {
      clients.delete(handle);
    }
  }),
);

app.get('/count', (c) => c.text(String(clients.size)));

export default {
  port: 8194,
  fetch: app.fetch,
};
