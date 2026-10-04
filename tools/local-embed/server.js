// 白泽记忆的本地向量化服务（OpenAI 兼容 /v1/embeddings）。
//
// 为什么有这个东西：NAS 上跑不了本地向量模型（2 核 / 2GB 内存 / 连不上 Docker Hub），
// 而免费云服务要么要实名认证、要么从国内连不上。所以把向量化放在这台常开的电脑上，
// 用 Transformers.js 在 CPU 上跑一个小模型，暴露成 OpenAI 兼容接口，NAS 后端直接调。
//
// 用法： node server.js [端口]
// 模型首次启动会从 hf-mirror 下载（国内可达），之后走本地缓存。

const http = require('http');
const path = require('path');
const { pipeline, env } = require('@xenova/transformers');

// HuggingFace 官方域名在国内不可达，走镜像（模型只下这一次，之后用本地缓存）
env.remoteHost = 'https://hf-mirror.com';
env.remotePathTemplate = '{model}/resolve/{revision}/';
env.cacheDir = path.join(__dirname, 'models');
env.allowLocalModels = false;

const MODEL = process.env.EMBED_MODEL || 'Xenova/bge-small-zh-v1.5';
const PORT = Number(process.argv[2] || 8484);
const HOST = process.env.EMBED_HOST || '0.0.0.0';
// BGE 系用 cls；别的模型（MiniLM / E5）一般是 mean
const POOLING = process.env.EMBED_POOLING || (MODEL.toLowerCase().includes('bge') ? 'cls' : 'mean');

let extractor = null;
let dimHint = 0;
// 模型推理串行：多路并发一起喂会把内存顶上去，队列化更稳
let queue = Promise.resolve();

async function loadModel() {
  if (extractor) return extractor;
  console.log('[local-embed] 正在加载模型 ' + MODEL + '（首次会从镜像下载，请稍等）…');
  extractor = await pipeline('feature-extraction', MODEL, { quantized: true });
  console.log('[local-embed] 模型就绪');
  return extractor;
}

async function embed(texts) {
  const p = await loadModel();
  // 池化方式必须跟模型的训练方式对上：BGE 系列（含 bge-m3）用 CLS，MiniLM/E5 用 mean。
  // 用错了不会报错，但相似度会整体失真（实测出现过"无关句比相关句还高"），所以做成可配的。
  const out = await p(texts, { pooling: POOLING, normalize: true });
  return out.tolist();
}

function json(res, code, body) {
  const raw = Buffer.from(JSON.stringify(body), 'utf8');
  res.writeHead(code, { 'Content-Type': 'application/json; charset=utf-8', 'Content-Length': raw.length });
  res.end(raw);
}

function readBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let size = 0;
    req.on('data', (c) => {
      size += c.length;
      if (size > 8 << 20) {
        reject(new Error('请求体过大'));
        req.destroy();
        return;
      }
      chunks.push(c);
    });
    req.on('end', () => resolve(Buffer.concat(chunks).toString('utf8')));
    req.on('error', reject);
  });
}

const server = http.createServer(async (req, res) => {
  const url = (req.url || '').split('?')[0];

  if (req.method === 'GET' && (url === '/health' || url === '/')) {
    json(res, 200, { ok: true, model: MODEL, ready: !!extractor, dim: dimHint });
    return;
  }
  if (req.method === 'GET' && url === '/v1/models') {
    json(res, 200, { object: 'list', data: [{ id: MODEL, object: 'model', owned_by: 'local' }] });
    return;
  }
  if (req.method !== 'POST' || url !== '/v1/embeddings') {
    json(res, 404, { error: { message: '只支持 POST /v1/embeddings' } });
    return;
  }

  let body;
  try {
    body = JSON.parse(await readBody(req));
  } catch (e) {
    json(res, 400, { error: { message: '请求体不是合法 JSON：' + e.message } });
    return;
  }
  const input = body.input;
  const texts = typeof input === 'string' ? [input] : Array.isArray(input) ? input : null;
  if (!texts || texts.length === 0) {
    json(res, 400, { error: { message: 'input 必须是字符串或字符串数组，且不能为空' } });
    return;
  }
  if (texts.some((t) => typeof t !== 'string' || t.trim() === '')) {
    json(res, 400, { error: { message: 'input 里有空字符串' } });
    return;
  }

  try {
    // 串行执行，避免并发把内存顶爆
    const run = queue.then(() => embed(texts));
    queue = run.catch(() => {});
    const vecs = await run;
    dimHint = vecs[0] ? vecs[0].length : dimHint;
    const data = vecs.map((v, i) => ({ object: 'embedding', index: i, embedding: v }));
    json(res, 200, {
      object: 'list',
      data,
      model: MODEL,
      usage: { prompt_tokens: texts.reduce((n, t) => n + t.length, 0), total_tokens: texts.reduce((n, t) => n + t.length, 0) },
    });
  } catch (e) {
    console.error('[local-embed] 向量化失败：', e);
    json(res, 500, { error: { message: '向量化失败：' + e.message } });
  }
});

server.listen(PORT, HOST, async () => {
  console.log('[local-embed] 监听 http://' + HOST + ':' + PORT + '/v1/embeddings（模型 ' + MODEL + '）');
  // 启动就把模型热起来，别让第一次请求等下载
  try {
    await loadModel();
    const v = await embed(['预热']);
    dimHint = v[0] ? v[0].length : 0;
    console.log('[local-embed] 维度 = ' + dimHint);
  } catch (e) {
    console.error('[local-embed] 预热失败（服务仍在，请求时会重试）：', e.message);
  }
});
