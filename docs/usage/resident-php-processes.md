# 常驻 PHP 进程

需要在多次调用间保留 PHP 进程内状态时，可以声明由 Runtime 监管的常驻 PHP
进程。它是独立的 PHP CLI 子进程，不是禅道的应用 Service，也不是 FrankenPHP
worker。

常驻进程适合维护长连接、保留进程内缓存或运行应用级事件循环。需要持久化、
自动重试、死信或跨节点单例执行的工作应使用 [Queue Bridge](queue-bridge.md)。

## 声明进程

在对应 `[[apps]]` 后声明 `[[apps.processes]]`：

```toml
[[apps]]
id = "zentao"
path = "/"
dir = "apps/zentao"
webRoot = "www"
enabled = true

[[apps.processes]]
name = "event-worker"
script = "service/event-worker.php"
required = false
readyTimeout = "30s"
requestTimeout = "15s"
stopTimeout = "20s"
maxPending = 32
```

`script` 和 `workDir` 是相对应用根目录的路径。省略其他字段时使用默认值。
`required = true` 的进程参与 Runtime readiness；非必需进程故障只会将状态标记
为 degraded。

## 实现常驻脚本

脚本使用 Runtime 提供的 SDK：

```php
<?php
require getenv('ZENTAO_PROCESS_SDK');

$worker = RuntimeProcessWorker::fromEnvironment();
$worker->run(function (array $request): array {
    switch($request['method'] ?? '')
    {
        case 'status':
            return ['ok' => true];
        case 'echo':
            return ['payload' => $request['payload'] ?? array()];
        default:
            throw new RuntimeProcessMethodNotFound();
    }
});
```

脚本使用与 Web 相同的 PHP、扩展和 ionCube Loader。需要调用禅道 Model 或 DAO
时，脚本还必须按应用现有 CLI 方式初始化禅道框架。

### 复杂示例：调用禅道 Model

下面的进程维护一个短时任务快照，并提供查询任务和写入评论两个方法。脚本位于
应用根目录的 `service/task-worker.php`：

```php
<?php
$root = dirname(__DIR__);
chdir($root);

include './framework/router.class.php';
include './framework/control.class.php';
include './framework/model.class.php';
include './framework/helper.class.php';

$app = router::createApp('pms', $root, 'router');
if(!$app->loadCommon()) throw new RuntimeException('failed to initialize ZenTao CLI context');

$taskModel   = $app->loadModel('task');
$userModel   = $app->loadModel('user');
$actionModel = $app->loadModel('action');

require getenv('ZENTAO_PROCESS_SDK');

$snapshotCache = array();
$worker = RuntimeProcessWorker::fromEnvironment();
$worker->run(function (array $request) use (&$snapshotCache, $taskModel, $userModel, $actionModel): array {
    $payload = is_array($request['payload'] ?? null) ? $request['payload'] : array();

    switch($request['method'] ?? '')
    {
        case 'task.snapshot':
            $taskID = (int)($payload['taskID'] ?? 0);
            if($taskID <= 0) throw new RuntimeProcessException('invalid_task', 'taskID is required');

            if(isset($snapshotCache[$taskID]) && time() - $snapshotCache[$taskID]['cachedAt'] < 30)
            {
                return $snapshotCache[$taskID]['value'];
            }

            $task = $taskModel->getByID($taskID);
            if(!$task) throw new RuntimeProcessException('task_not_found', 'task was not found');

            $assignedTo = null;
            if(!empty($task->assignedTo))
            {
                $user = $userModel->getById($task->assignedTo);
                if($user) $assignedTo = array('account' => $user->account, 'realname' => $user->realname);
            }

            $result = array(
                'id'         => (int)$task->id,
                'name'       => $task->name,
                'status'     => $task->status,
                'assignedTo' => $assignedTo,
                'snapshotAt' => date('c'),
            );
            $snapshotCache[$taskID] = array('cachedAt' => time(), 'value' => $result);
            return $result;

        case 'task.comment':
            $taskID  = (int)($payload['taskID'] ?? 0);
            $comment = trim((string)($payload['comment'] ?? ''));
            if($taskID <= 0 || $comment === '') throw new RuntimeProcessException('invalid_request', 'taskID and comment are required');
            if(!$taskModel->getByID($taskID)) throw new RuntimeProcessException('task_not_found', 'task was not found');

            $actionID = $actionModel->create('task', $taskID, 'commented', $comment, '', 'runtime-worker');
            if(!$actionID) throw new RuntimeProcessException('action_failed', 'failed to create task comment');

            unset($snapshotCache[$taskID]);
            return array('actionID' => (int)$actionID);

        default:
            throw new RuntimeProcessMethodNotFound();
    }
});
```

对应配置：

```toml
[[apps.processes]]
name = "task-worker"
script = "service/task-worker.php"
requestTimeout = "30s"
```

示例直接复用禅道的 `taskModel`、`userModel` 和 `actionModel`。评论的
`actor` 显式设为 `runtime-worker`；实际业务应根据调用场景记录正确的操作人，
并在应用 Service 层完成权限校验。写入类 Handler 仍需要自行保证幂等，不能依赖
Process RPC 自动去重。该示例需要在已完成安装、数据库配置有效的禅道应用目录中
运行；`snapshotCache` 只存在于当前进程内，进程重启后会丢失。

## 从应用调用

应用 Web 代码通过 Runtime Process SDK 调用当前应用的进程：

```php
require getenv('ZENTAO_PROCESS_SDK');

$client = RuntimeProcessClient::forCurrentApp();
$result = $client->call('event-worker', 'status', [], 5000);
```

调用上面的禅道 Model 示例：

```php
$snapshot = $client->call('task-worker', 'task.snapshot', ['taskID' => 7], 5000);
$comment  = $client->call('task-worker', 'task.comment', [
    'taskID'  => 7,
    'comment' => 'Resident process health check',
], 5000);
```

建议在应用 Service 层封装 SDK，不要让业务模块重复读取环境变量或处理协议。

## 运行环境

Runtime 注入以下 Web 侧变量：

```text
ZENTAO_PROCESS_BRIDGE_URL
ZENTAO_PROCESS_APP_ID
ZENTAO_PROCESS_CALL_TOKEN
ZENTAO_PROCESS_SDK
ZENTAO_PROCESS_SCHEMA
```

常驻脚本还会获得当前进程的实例 ID 和 worker Token。Web 进程不会获得 worker
Token。Runtime 管理的变量不能被应用 `env` 覆盖。

## 调用语义

进程调用是同步、非持久化 RPC：

- Runtime 不自动重试。
- 请求已交付后超时返回 `outcome_unknown`，调用方必须使用幂等键。
- 进程崩溃时，未交付请求返回 `not_delivered`，已交付但无结果的请求返回
  `outcome_unknown`。
- 单个进程首版串行处理请求，并发请求在 `maxPending` 范围内排队。

## 管理进程

查看进程状态：

```bash
./bin/zentao processes
```

重启单个进程：

```bash
./bin/zentao process-restart --app zentao --process event-worker
```

`process-restart` 是异步操作，返回后应再次执行 `processes`，等待状态恢复为
`ready`。

进程配置变化需要重启 Runtime。仅更新脚本文件时，可以重启对应进程以清除 CLI
OPcache；单项重启不会重新加载 `runtime.toml`。
