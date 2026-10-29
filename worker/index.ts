// Worker in front of the hosted site (dynasty.williamfowle.com).
//
// - Every request must carry a valid Cloudflare Access JWT. Access sits at the
//   edge already; this check stops anything that reaches the Worker another way.
// - /media/* is served straight from the R2 bucket (EA assets stay behind Access).
// - Everything else goes to the Go container running `dsn serve -hosted`.
// - The container reaches the bucket through the virtual host media.r2: it reads
//   _manifest.txt to index media and PUTs article image uploads under articles/.

import { Container, ContainerProxy, getContainer } from "@cloudflare/containers";
import { createRemoteJWKSet, jwtVerify } from "jose";

export { ContainerProxy };

interface Env {
	SITE: DurableObjectNamespace<Site>;
	MEDIA: R2Bucket;
	DSN_DB: string; // secret: hosted Postgres URL
	ACCESS_TEAM_DOMAIN: string; // e.g. yourteam.cloudflareaccess.com
	ACCESS_AUD: string; // the site's Access application AUD tag
	ACCESS_MCP_AUD: string; // AUD of the /mcp-only Access app agents sign in to
}

const mediaBridge = async (request: Request, env: Env): Promise<Response> => {
	const key = decodeURIComponent(new URL(request.url).pathname.slice(1));
	if (request.method === "GET") {
		const obj = await env.MEDIA.get(key);
		return obj ? new Response(obj.body) : new Response("not found", { status: 404 });
	}
	// Only article uploads are written from the site; game media comes from git.
	if (request.method === "PUT" && key.startsWith("articles/") && !key.includes("..")) {
		await env.MEDIA.put(key, request.body, {
			httpMetadata: { contentType: request.headers.get("content-type") ?? undefined },
		});
		return new Response(null, { status: 201 });
	}
	return new Response("forbidden", { status: 403 });
};

export class Site extends Container<Env> {
	defaultPort = 8080;
	sleepAfter = "15m";

	constructor(ctx: DurableObjectState, env: Env) {
		super(ctx, env);
		this.envVars = { DSN_DB: env.DSN_DB };
	}
}

// Assigned, not declared as a static class field: Container's static setter
// registers handlers by class name, and a class field would bypass it.
Site.outboundByHost = { "media.r2": mediaBridge };

let jwks: ReturnType<typeof createRemoteJWKSet> | undefined;

// accessAllowed checks the Access JWT. A token for the site app opens every
// path; a token for the /mcp app (agents' service token) opens only /mcp.
async function accessAllowed(request: Request, env: Env): Promise<boolean> {
	const token = request.headers.get("cf-access-jwt-assertion");
	if (!token || !env.ACCESS_TEAM_DOMAIN || !env.ACCESS_AUD) {
		return false;
	}
	const issuer = `https://${env.ACCESS_TEAM_DOMAIN}`;
	jwks ??= createRemoteJWKSet(new URL(`${issuer}/cdn-cgi/access/certs`));
	const audiences = [env.ACCESS_AUD, env.ACCESS_MCP_AUD].filter(Boolean);
	try {
		const { payload } = await jwtVerify(token, jwks, { issuer, audience: audiences });
		const aud = Array.isArray(payload.aud) ? payload.aud : [payload.aud];
		return aud.includes(env.ACCESS_AUD) || new URL(request.url).pathname === "/mcp";
	} catch {
		return false;
	}
}

async function serveMedia(request: Request, env: Env, key: string): Promise<Response> {
	const obj = await env.MEDIA.get(key, { onlyIf: request.headers, range: request.headers });
	if (!obj) {
		return new Response("not found", { status: 404 });
	}
	const headers = new Headers();
	obj.writeHttpMetadata(headers);
	headers.set("etag", obj.httpEtag);
	headers.set("cache-control", "private, max-age=86400");
	if (!("body" in obj)) {
		return new Response(null, { status: 304, headers });
	}
	return new Response(request.method === "HEAD" ? null : obj.body, { headers });
}

export default {
	async fetch(request: Request, env: Env): Promise<Response> {
		if (!(await accessAllowed(request, env))) {
			return new Response("forbidden", { status: 403 });
		}
		const url = new URL(request.url);
		if ((request.method === "GET" || request.method === "HEAD") && url.pathname.startsWith("/media/")) {
			return serveMedia(request, env, decodeURIComponent(url.pathname.slice("/media/".length)));
		}
		return getContainer(env.SITE).fetch(request);
	},
} satisfies ExportedHandler<Env>;
