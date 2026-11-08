# Account-Web HTTP tunnel v1

桌面用自身 gateway Bearer 连接主 Origin 的 `GET /v1/ws/web-gateway`，子协议为 `bridge-web-proxy-v1`。不接受浏览器 Origin 或查询参数。服务端检查 gateway、原生登录会话和账号资格。此协议使用 TLS，云端可见明文，与手机 Noise v2 独立。

每个文本控制记录最多 16 KiB；二进制记录为 `0x01 | stream_id:uint64_be | payload`，payload 为 1–32768 字节。stream_id 由云递增分配，范围 1–2^53−1，一个连接内不重用。

请求控制为 `request`：`stream_id`、`session_id`、`account_id`、`method`、`path`、`length`、`content_type`、`headers`、`base`、`origin`、`csrf`、`expires`。服务器生成认证、电脑前缀和会话元数据；用户只能决定被白名单允许的相对路径、GET/POST 及请求体。expires 是 UTC Unix 秒。

桌面为有请求体的流发送 `credit`（count=1–4），云按窗口发送二进制体，最后发送 `request_end`。桌面经固定私有回环 HTTP 服务执行一次请求，返回 `response_start`（status、headers）、二进制体和 `response_end`。云在收到响应头后授予 4 个响应信用，每写出一个块补 1 个。请求阶段剩余信用最多 4 个。队列每流最多 8 条记录，溢出只取消该流。

`cancel` 取消相应 HTTP 流；`stream_error` 表示失败，已开始响应时终止 HTTP 连接，禁止把截断正文当成功。已结束流的尾随信用与取消可忽略，不重开或重放流。`close_session` 携带 session_id，桌面销毁该浏览器的临时本地会话并取消其请求。断开 WSS 清理全部该连接上的 Web 会话；只有出站连接自动重连，业务请求从不重放。

请求头仅允许 Range、If-Range、If-None-Match 和 Content-Type；响应只转发 Content-Type、Content-Length、Content-Disposition、ETag、Cache-Control、Content-Range、Accept-Ranges、CSP、X-Frame-Options。桌面固定资源经过命名空间适配，缓存策略转为 private，HTML 不缓存；不向云返回 Set-Cookie。响应长度、状态、帧大小和正文总量均受校验。
