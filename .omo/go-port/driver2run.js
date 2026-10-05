const url = new URL("./driver2.js", import.meta.url);
url.searchParams.set("t", String(Date.now()));
export const step = (await import(url.href)).step;
