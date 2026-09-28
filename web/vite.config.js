var _a, _b;
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
// Vite dev server proxies API + healthz to the Go backend so that the SPA can
// be developed standalone with `npm run dev`. In prod the same paths are
// served by nginx via the upstream block in nginx.conf.
// 开发期后端地址：默认 localhost:8080，可用 QUANT4DAD_DEV_BACKEND 环境变量覆盖
// （如对接部署机某分支后端：`QUANT4DAD_DEV_BACKEND=http://localhost:9021 npm run dev`）。
// 用 globalThis 读 process.env，免去仅为类型而引入 @types/node 的依赖。
var env = (_b = (_a = globalThis.process) === null || _a === void 0 ? void 0 : _a.env) !== null && _b !== void 0 ? _b : {};
var backend = env.QUANT4DAD_DEV_BACKEND || 'http://localhost:8080';
export default defineConfig({
    plugins: [react()],
    server: {
        host: '0.0.0.0',
        port: 5173,
        proxy: {
            '/api': { target: backend, changeOrigin: true },
            '/healthz': { target: backend, changeOrigin: true },
        },
    },
    build: { outDir: 'dist', sourcemap: false },
});
