import assert from 'node:assert/strict';
import { test } from 'node:test';
import { copyText } from './copy.ts';

const URL_TO_COPY = 'https://mcp.example.test/mcp';

test('copyText reports "copied" and writes the exact text when the clipboard accepts it', async () => {
  const written: string[] = [];
  const clipboard = {
    writeText: async (text: string) => {
      written.push(text);
    },
  };

  assert.equal(await copyText(URL_TO_COPY, clipboard), 'copied');
  assert.deepEqual(written, [URL_TO_COPY]);
});

test('copyText reports "unavailable" when the clipboard rejects the write', async () => {
  const clipboard = { writeText: () => Promise.reject(new DOMException('denied', 'NotAllowedError')) };

  assert.equal(await copyText(URL_TO_COPY, clipboard), 'unavailable');
});

test('copyText reports "unavailable" when writeText throws synchronously', async () => {
  const clipboard = {
    writeText: (): Promise<void> => {
      throw new TypeError('insecure context');
    },
  };

  assert.equal(await copyText(URL_TO_COPY, clipboard), 'unavailable');
});

test('copyText reports "unavailable" when there is no clipboard API', async () => {
  assert.equal(await copyText(URL_TO_COPY, undefined), 'unavailable');
});
