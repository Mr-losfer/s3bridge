# S3 开放接口文档

S3Bridge 把「各家第三方上传接口」统一暴露成一套 **S3 兼容的对象存储接口**。任何支持自定义 Endpoint 的 S3 SDK / 工具都可以直接接入，无需改造业务代码。

---

## 1. 接入信息

| 项目 | 值 |
| --- | --- |
| Endpoint | `http(s)://<host>/s3` |
| Region | `us-east-1`（默认值，可用启动环境配置） |
| 寻址风格 | **Path-style**：`/{bucket}/{key}` |
| 签名 | AWS Signature Version 4（`Authorization` 头） |
| 协议版本 | S3 REST API（XML 响应） |

后台「API 密钥」页面会显示当前部署的实际 Endpoint、Region 与示例命令。

---

## 2. 凭据：每个用户用自己的 API Key

> **多租户隔离的核心**：每个用户在后台创建自己的 API Key，这把密钥同时是 S3 的 `AccessKeyId` / `SecretAccessKey` 与 REST 的 Bearer 令牌。

创建方式：登录后台 →「API 密钥」→「创建密钥」。明文**不需要一次性抄录**：随时在列表点「查看」即可取回；「MCP 能力」页顶部也会直接显示当前登录用户的密钥明文与已填好真实凭据的对接代码，一键复制即可。

```json
{
  "id": "key_xxx",
  "accessKeyId": "AK_9f2c...",
  "secretAccessKey": "sk_xxx（随时可再次查看）",
  "note": "SecretAccessKey 已加密保存，可随时用 apikey_reveal 再次取回。",
  "s3": { "endpoint": "/s3", "region": "us-east-1" }
}
```

要点：

- **明文可随时查看**。服务端用 AES-256-GCM 加密存储密文，鉴权时用 SHA-256 摘要比对，日志与状态文件中没有明文；登录后的本人或管理员可按需解密查看。
- 支持设置**有效期**（`expiresInDays`，0 表示永不过期）与**作用域**（`scopes`，留空继承用户全部权限），适合给 CI、第三方应用下发最小权限密钥。
- 密钥随时可撤销，撤销后该密钥签名的所有 S3 请求立即返回 `AccessDenied`。
- 用户只能看到/撤销自己的密钥；管理员可见全部。

### 随时取回明文

```http
GET /api/keys/{id}/secret
Authorization: Bearer <会话令牌或该用户的 API Key>
```

返回 `id` / `name` / `userId` / `accessKeyId` / `secretAccessKey` / `scopes` / `revoked` / `region` 等字段；越权返回 `403`，密钥不存在返回 `404`。MCP 对应工具为 `apikey_reveal`，后台「API 密钥」页的「查看」按钮与「MCP 能力」页顶部卡片都走这条通路。

### 权限映射

| S3 操作 | 需要的权限 |
| --- | --- |
| `ListBuckets` / `HeadBucket` | `s3:access` |
| `ListObjectsV2` / `HeadObject` / `GetObject` | `upload:read` |
| `PutObject` / `DeleteObject` | `upload:write` |
| `CreateBucket` / `DeleteBucket` | `bucket:write` |

角色默认权限：`admin` 全部；`editor` 含写入；`viewer` 只读。管理员可在「用户与权限」页逐项授予或撤销。

---

## 3. 支持的 API

| 操作 | 请求 | 说明 |
| --- | --- | --- |
| ListBuckets | `GET /s3/` | 列出当前用户可见的桶（自己拥有的 + 公开读的；管理员全部） |
| CreateBucket | `PUT /s3/{bucket}` | 创建桶，桶名需符合 `^[a-z0-9._-]{3,63}$` |
| HeadBucket | `HEAD /s3/{bucket}` | 判断桶是否存在/可访问 |
| DeleteBucket | `DELETE /s3/{bucket}` | 仅空桶可删除 |
| ListObjectsV2 | `GET /s3/{bucket}?list-type=2&prefix=&max-keys=` | 列出对象，支持 `prefix`、`max-keys`、`continuation-token` |
| ListObjects (v1) | `GET /s3/{bucket}?prefix=&marker=` | 兼容旧版客户端 |
| PutObject | `PUT /s3/{bucket}/{key}` | 上传对象，走插件链路或本地归档 |
| GetObject | `GET /s3/{bucket}/{key}` | 下载对象，支持 `Range` |
| HeadObject | `HEAD /s3/{bucket}/{key}` | 返回元数据（Content-Type / Content-Length / ETag） |
| DeleteObject | `DELETE /s3/{bucket}/{key}` | 删除对象 |

响应为标准 S3 XML，例如：

```xml
<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>default</Name>
  <Prefix></Prefix>
  <KeyCount>2</KeyCount>
  <MaxKeys>1000</MaxKeys>
  <IsTruncated>false</IsTruncated>
  <Contents>
    <Key>image/202611/a1b2.png</Key>
    <LastModified>2026-11-08T23:58:31Z</LastModified>
    <ETag>"9f2c..."</ETag>
    <Size>2033070</Size>
    <StorageClass>STANDARD</StorageClass>
  </Contents>
</ListBucketResult>
```

错误同样是标准 S3 XML：

```xml
<Error>
  <Code>SignatureDoesNotMatch</Code>
  <Message>签名校验失败</Message>
  <RequestId>...</RequestId>
</Error>
```

常见错误码：`AccessDenied`、`SignatureDoesNotMatch`、`RequestTimeTooSkewed`（±15 分钟时钟偏移）、`NoSuchBucket`、`NoSuchKey`、`BucketAlreadyExists`、`BucketNotEmpty`、`InvalidBucketName`。

---

## 4. 快速开始

### aws-cli v2

```bash
export AWS_ACCESS_KEY_ID=AK_9f2c...
export AWS_SECRET_ACCESS_KEY=<后台随时可查看的明文>
export AWS_DEFAULT_REGION=us-east-1
ENDPOINT=http://localhost:8080/s3

aws --endpoint-url "$ENDPOINT" s3 ls
aws --endpoint-url "$ENDPOINT" s3 mb s3://assets
aws --endpoint-url "$ENDPOINT" s3 cp ./banner.png s3://assets/images/banner.png
aws --endpoint-url "$ENDPOINT" s3 ls s3://assets --recursive
aws --endpoint-url "$ENDPOINT" s3 cp s3://assets/images/banner.png ./downloaded.png
aws --endpoint-url "$ENDPOINT" s3 rm s3://assets/images/banner.png
```

### Python (boto3)

```python
import boto3
from botocore.config import Config

s3 = boto3.client(
    "s3",
    endpoint_url="http://localhost:8080/s3",
    region_name="us-east-1",
    aws_access_key_id="AK_9f2c...",
    aws_secret_access_key="<后台随时可查看的明文>",
    config=Config(s3={"addressing_style": "path"}),
)

print([b["Name"] for b in s3.list_buckets()["Buckets"]])
s3.put_object(Bucket="assets", Key="images/a.png", Body=open("a.png", "rb").read(), ContentType="image/png")
obj = s3.get_object(Bucket="assets", Key="images/a.png")
print(obj["ContentType"], obj["ContentLength"])
for o in s3.list_objects_v2(Bucket="assets", Prefix="images/").get("Contents", []):
    print(o["Key"], o["Size"])
```

### Go (aws-sdk-go-v2)

```go
cfg, err := config.LoadDefaultConfig(ctx,
    config.WithRegion("us-east-1"),
    config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
        "AK_9f2c...", "<后台随时可查看的明文>", "")),
)
client := s3.NewFromConfig(cfg, func(o *s3.Options) {
    o.BaseEndpoint = aws.String("http://localhost:8080/s3")
    o.UsePathStyle = true
})
out, err := client.ListBuckets(ctx, &s3.ListBucketsInput{})
```

### Node.js (@aws-sdk/client-s3)

```js
import { S3Client, ListBucketsCommand, PutObjectCommand } from "@aws-sdk/client-s3";

const s3 = new S3Client({
  region: "us-east-1",
  endpoint: "http://localhost:8080/s3",
  forcePathStyle: true,
  credentials: {
    accessKeyId: "AK_9f2c...",
    secretAccessKey: "<后台随时可查看的明文>",
  },
});

console.log(await s3.send(new ListBucketsCommand({})));
await s3.send(new PutObjectCommand({ Bucket: "assets", Key: "a.txt", Body: "hello" }));
```

### curl（带 SigV4）

手写 SigV4 较繁琐，建议用 `awscli` 的 `--debug` 观察签名，或用 `awscurl`：

```bash
pip install awscurl
awscurl --service s3 --region us-east-1 \
  --access_key AK_9f2c... --secret_key '<后台随时可查看的明文>' \
  http://localhost:8080/s3/
```

---

## 5. SigV4 实现要点

服务端完整实现了 AWS Signature Version 4 校验，供自研客户端参考：

1. **CanonicalRequest**：`METHOD\nCanonicalURI\nCanonicalQueryString\nCanonicalHeaders\nSignedHeaders\nHexEncode(SHA256(Payload))`
   - `CanonicalURI` 为 `/s3/{bucket}/{key}` 的 URI 编码形式；
   - `CanonicalQueryString` 参数按 key 排序并 URL 编码；
   - 必须签 `host` 与 `x-amz-date`（或 `date`）。
2. **StringToSign**：`AWS4-HMAC-SHA256\n<AMZDate>\n<Scope>\nHexEncode(SHA256(CanonicalRequest))`
   - `Scope = <YYYYMMDD>/<region>/s3/aws4_request`。
3. **SigningKey**：`HMAC(HMAC(HMAC(HMAC("AWS4"+SK, date), region), "s3"), "aws4_request")`。
4. **Authorization**：
   ```
   AWS4-HMAC-SHA256 Credential=<AccessKeyId>/<Scope>, SignedHeaders=<...>, Signature=<hex>
   ```
5. 服务端用 `AccessKeyId` 查到密钥记录，取其 SHA-256 摘要对应的密文解密后重算签名，用 `hmac.Equal` 常量时间比较；时间戳偏移超过 15 分钟返回 `RequestTimeTooSkewed`。

> 同时支持 **Presigned URL**：查询参数中带 `X-Amz-Algorithm=AWS4-HMAC-SHA256`、`X-Amz-Credential`、`X-Amz-Signature`、`X-Amz-Expires` 等，可用于生成临时下载链接。

---

## 6. 存储桶与归档

- 桶是独立命名空间。后台「存储桶」页可创建、编辑说明、设置**公开读**、删除（需先清空对象）。
- 每次上传都会先判定 **文件类型**：默认按类型在所有已启用插件中自动选择（同类型内**权重高者优先**，见插件配置的 `priority` / `fileTypes`），也可在后台测试台显式指定插件。
- 每次上传都会按 **文件类型** 自动归档：`image` / `video` / `audio` / `document` / `archive` / `other`。桶内对象键为 `{category}/{YYYYMM}/{hash}_{name}`，磁盘归档路径为 `{bucket}/{category}/{YYYYMM}/`；因此可用 `prefix=image/` 按类型列举。
- 后台「上传列表」可按 **bucket + 文件类型 + 来源插件 + 关键字** 组合过滤，并直接预览图片、下载、复制 `s3://` URI。
- 对象键会做路径穿越清洗（`..`、绝对路径、盘符均被拒绝）。
- `PutObject` 落库时会记录 SHA-256，作为 `ETag` 返回，便于客户端校验完整性。

---

## 7. 与 REST / MCP 的关系

三条通道读写同一份数据，权限模型一致：

```
后台页面  ── REST /api/* ──┐
AI 客户端 ── MCP  /mcp ───┼──► 同一套 Store / 权限 / 审计
S3 客户端 ── S3   /s3 ────┘
```

例如：通过 `aws s3 cp` 上传的对象，会立刻出现在后台「上传列表」和 MCP `uploads_list` 结果中；后台创建的桶也能被 `aws s3 mb` 之外的所有 S3 工具看到。

---

## 8. 安全建议

- 生产环境务必 **HTTPS**：SigV4 不加密请求体，明文凭据仅在签名派生中使用，但传输层仍需 TLS。
- 给每个应用/每个人单独发密钥，设置最小 `scopes` 与合理有效期，便于独立撤销。
- 定期轮换密钥；不要把它写进代码仓库、CI 日志或前端。
- 通过反向代理限制 `/s3` 的来源 IP，并对 `S3BRIDGE_MAX_UPLOAD_BYTES` 设置合理上限。
