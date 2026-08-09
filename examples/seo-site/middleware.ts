// Request middleware executes before cache and origin routing. Returning
// fetch(request) continues to the Go application through the platform's
// controlled outbound path.
export default function middleware(request: Request): Response | Promise<Response> {
  const url = new URL(request.url)
  if (url.pathname === "/articles/old-portable-react") {
    return Response.redirect(new URL("/articles/portable-react", request.url), 308)
  }
  return fetch(request)
}
