import http from "http";

const hostname = "127.0.0.1";
const port = parseInt(process.env.PR_PORT, 10);

const server = http.createServer((req, res) => {
  console.log(`req logging from ${process.env.PR_PORT}`);

  setTimeout(() => {
    res.statusCode = 200;

    if (req.url == "/health") {
      res.setHeader("Content-Type", "application/json");
      res.end(JSON.stringify({ status: "ok" }));
      return;
    }

    res.setHeader("Content-Type", "text/plain");
    res.end(
      `hello from port: ${process.env.PR_PORT}, version: ${process.env.PR_VERSION}`
    );
  }, 1000); // Simulate a slow response.
});

server.listen(port, hostname, () => {
  console.log(`Server running at http://${hostname}:${port}/`);
});
