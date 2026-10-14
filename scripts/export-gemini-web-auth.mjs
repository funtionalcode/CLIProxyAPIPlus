// Run with ego-browser nodejs after logging in and returning browser control.
// Only Gemini's two required session cookies are exported; never print their values.
const fs = await import("node:fs/promises");
const path = await import("node:path");
const options = globalThis.cpaGeminiExport || {};
const spaceId = Number(options.spaceId);
if (!Number.isInteger(spaceId) || spaceId <= 0) {
  throw new Error("Set cpaGeminiExport.spaceId to the logged-in Ego task space ID");
}
const task = await taskSpace(spaceId);
const page = task.page(options.page || "p1");
if (new URL(await page.url()).origin !== "https://gemini.google.com") {
  throw new Error("The selected page must be the logged-in Gemini website");
}
const { cookies } = await page.cdp("Network.getCookies", { urls: ["https://gemini.google.com/"] });
const getCookie = (name) => cookies.find((cookie) => cookie.name === name)?.value || "";
const psid = getCookie("__Secure-1PSID");
if (!psid) throw new Error("Gemini session cookie is missing; finish Google login first");
if (!options.authFile || !path.isAbsolute(options.authFile)) {
  throw new Error("Set cpaGeminiExport.authFile to an absolute output path");
}
const output = options.authFile;
const auth = {
  type: "gemini-web",
  email: options.account || "gemini-web-local",
  secure_1psid: psid,
  secure_1psidts: getCookie("__Secure-1PSIDTS"),
};
if (options.proxy) auth.proxy_url = options.proxy;
await fs.mkdir(path.dirname(output), { recursive: true, mode: 0o700 });
// Exclusive creation protects an existing account file from accidental replacement.
const file = await fs.open(output, "wx", 0o600);
try { await file.writeFile(JSON.stringify(auth, null, 2) + "\n"); }
finally { await file.close(); }
console.log({ authFile: output, provider: auth.type, cookieValuesPrinted: false });
