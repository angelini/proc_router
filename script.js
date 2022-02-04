const http = require('http');

const hostname = '127.0.0.1';
const port = parseInt(process.env.PR_PORT, 10);

const server = http.createServer((req, res) => {
  console.log(`req logging from ${process.env.PR_PORT}`);
  res.statusCode = 200;
  res.setHeader('Content-Type', 'text/plain');
  res.end(`hello from port: ${process.env.PR_PORT}, version: ${process.env.PR_VERSION}`);
});

server.listen(port, hostname, () => {
  console.log(`Server running at http://${hostname}:${port}/`);
});
