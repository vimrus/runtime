<?php
declare(strict_types=1);

/**
 * Runtime resident-process SDK.
 *
 * The file is loaded explicitly from ZENTAO_PROCESS_SDK and has no Composer or
 * framework dependency.
 */
class RuntimeProcessException extends RuntimeException
{
    public string $processCode;
    public string $delivery;
    public int $httpStatus;

    public function __construct(string $processCode, string $message, string $delivery = '', int $httpStatus = 0)
    {
        parent::__construct($message);
        $this->processCode = $processCode;
        $this->delivery = $delivery;
        $this->httpStatus = $httpStatus;
    }
}

final class RuntimeProcessMethodNotFound extends RuntimeProcessException
{
    public function __construct(string $message = 'process method not found')
    {
        parent::__construct('method_not_found', $message);
    }
}

final class RuntimeProcessClient
{
    private string $baseURL;
    private string $appID;
    private string $token;
    private int $schema;

    private function __construct(string $baseURL, string $appID, string $token, int $schema)
    {
        $this->baseURL = rtrim($baseURL, '/');
        $this->appID = $appID;
        $this->token = $token;
        $this->schema = $schema;
    }

    public static function forCurrentApp(): self
    {
        $baseURL = getenv('ZENTAO_PROCESS_BRIDGE_URL');
        $appID = getenv('ZENTAO_PROCESS_APP_ID');
        $token = getenv('ZENTAO_PROCESS_CALL_TOKEN');
        $schema = getenv('ZENTAO_PROCESS_SCHEMA');
        if(!is_string($baseURL) || $baseURL === '' ||
            !is_string($appID) || $appID === '' ||
            !is_string($token) || $token === '' ||
            !is_string($schema) || $schema === '')
        {
            throw new RuntimeProcessException('process_unavailable', 'runtime process bridge is not configured', 'not_delivered');
        }
        return new self($baseURL, $appID, $token, (int)$schema);
    }

    public function call(string $processName, string $method, mixed $payload = [], ?int $timeoutMs = null): mixed
    {
        if($processName === '' || $method === '')
        {
            throw new InvalidArgumentException('process name and method are required');
        }
        $requestID = self::requestID();
        $body = [
            'schema' => $this->schema,
            'requestId' => $requestID,
            'method' => $method,
            'payload' => $payload,
        ];
        if($timeoutMs !== null) $body['timeoutMs'] = $timeoutMs;

        $path = '/apps/' . rawurlencode($this->appID) . '/' . rawurlencode($processName) . '/call';
        $timeout = $timeoutMs === null ? 310 : max(1, (int)ceil($timeoutMs / 1000) + 5);
        [$status, $data] = RuntimeProcessTransport::post(
            $this->baseURL . $path,
            $body,
            [
                'X-Zentao-Process-Token: ' . $this->token,
                'X-Zentao-Process-Schema: ' . $this->schema,
            ],
            $timeout
        );
        if($status < 200 || $status >= 300)
        {
            $error = is_array($data) && isset($data['error']) ? $data['error'] : [];
            if(isset($data['code']))
            {
                // Wire errors are flat; accept both representations for
                // forward-compatible bridge diagnostics.
                $error = $data;
            }
            throw new RuntimeProcessException(
                (string)($error['code'] ?? 'process_request_failed'),
                (string)($error['message'] ?? 'process request failed'),
                (string)($error['delivery'] ?? ''),
                $status
            );
        }
        return is_array($data) && array_key_exists('result', $data) ? $data['result'] : null;
    }

    private static function requestID(): string
    {
        return bin2hex(random_bytes(16));
    }
}

final class RuntimeProcessWorker
{
    private string $baseURL;
    private string $appID;
    private string $processName;
    private string $instanceID;
    private string $token;
    private int $schema;

    private function __construct(
        string $baseURL,
        string $appID,
        string $processName,
        string $instanceID,
        string $token,
        int $schema
    )
    {
        $this->baseURL = rtrim($baseURL, '/');
        $this->appID = $appID;
        $this->processName = $processName;
        $this->instanceID = $instanceID;
        $this->token = $token;
        $this->schema = $schema;
    }

    public static function fromEnvironment(): self
    {
        $values = [
            'bridge' => getenv('ZENTAO_PROCESS_BRIDGE_URL'),
            'app' => getenv('ZENTAO_PROCESS_APP_ID'),
            'process' => getenv('ZENTAO_PROCESS_NAME'),
            'instance' => getenv('ZENTAO_PROCESS_INSTANCE_ID'),
            'token' => getenv('ZENTAO_PROCESS_WORKER_TOKEN'),
            'schema' => getenv('ZENTAO_PROCESS_SCHEMA'),
        ];
        foreach($values as $name => $value)
        {
            if(!is_string($value) || $value === '')
            {
                throw new RuntimeProcessException('process_unavailable', 'process worker environment is incomplete: ' . $name, 'not_delivered');
            }
        }
        return new self(
            $values['bridge'],
            $values['app'],
            $values['process'],
            $values['instance'],
            $values['token'],
            (int)$values['schema']
        );
    }

    public function run(callable $handler): void
    {
        $this->post('ready', ['schema' => $this->schema, 'instanceId' => $this->instanceID], 10);
        while(true)
        {
            $response = $this->receiveNext();
            if($response === null) continue;
            if(($response['type'] ?? '') === 'shutdown')
            {
                return;
            }
            if(($response['type'] ?? '') !== 'request' || !isset($response['request']) || !is_array($response['request']))
            {
                throw new RuntimeProcessException('invalid_protocol', 'process bridge returned an invalid work item');
            }

            $request = $response['request'];
            $reply = [
                'schema' => $this->schema,
                'instanceId' => $this->instanceID,
                'requestId' => (string)($request['requestId'] ?? ''),
            ];
            try
            {
                $result = $handler($request);
                $reply['result'] = $result;
            }
            catch(RuntimeProcessMethodNotFound $exception)
            {
                $reply['error'] = ['code' => 'method_not_found', 'message' => $exception->getMessage()];
            }
            catch(RuntimeProcessException $exception)
            {
                $reply['error'] = ['code' => $exception->processCode, 'message' => $exception->getMessage()];
            }
            catch(Throwable $exception)
            {
                $reply['error'] = [
                    'code' => 'handler_error',
                    'message' => self::safeMessage($exception->getMessage()),
                ];
            }
            $this->post('reply', $reply, 10);
        }
    }

    public function name(): string { return $this->processName; }
    public function instanceID(): string { return $this->instanceID; }

    private function receiveNext(): ?array
    {
        [$status, $data] = RuntimeProcessTransport::post(
            $this->endpoint('next'),
            ['schema' => $this->schema, 'instanceId' => $this->instanceID],
            $this->headers(),
            30
        );
        if($status === 204) return null;
        if($status < 200 || $status >= 300)
        {
            throw self::errorFromResponse($status, $data);
        }
        if(!is_array($data)) throw new RuntimeProcessException('invalid_protocol', 'process bridge returned invalid JSON');
        return $data;
    }

    private function post(string $endpoint, array $body, int $timeout): mixed
    {
        [$status, $data] = RuntimeProcessTransport::post(
            $this->endpoint($endpoint),
            $body,
            $this->headers(),
            $timeout
        );
        if($status < 200 || $status >= 300) throw self::errorFromResponse($status, $data);
        return $data;
    }

    private function endpoint(string $endpoint): string
    {
        return $this->baseURL . '/apps/' . rawurlencode($this->appID) . '/' .
            rawurlencode($this->processName) . '/' . $endpoint;
    }

    private function headers(): array
    {
        return [
            'X-Zentao-Process-Token: ' . $this->token,
            'X-Zentao-Process-Schema: ' . $this->schema,
        ];
    }

    private static function errorFromResponse(int $status, mixed $data): RuntimeProcessException
    {
        $error = is_array($data) ? $data : [];
        return new RuntimeProcessException(
            (string)($error['code'] ?? 'process_request_failed'),
            (string)($error['message'] ?? 'process request failed'),
            (string)($error['delivery'] ?? ''),
            $status
        );
    }

    private static function safeMessage(string $message): string
    {
        $message = trim($message);
        return $message === '' ? 'handler failed' : substr($message, 0, 256);
    }
}

final class RuntimeProcessTransport
{
    private const MaxBytes = 2097153;

    public static function post(string $url, array $body, array $headers, int $timeout): array
    {
        $json = json_encode($body, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE);
        if($json === false)
        {
            throw new RuntimeProcessException('invalid_request', 'could not encode process request');
        }
        if(strlen($json) > 1048576)
        {
            throw new RuntimeProcessException('request_too_large', 'process request exceeds 1 MiB');
        }
        $headers = array_merge(
            ['Content-Type: application/json', 'Accept: application/json', 'Connection: close'],
            $headers
        );
        $context = stream_context_create([
            'http' => [
                'method' => 'POST',
                'header' => implode("\r\n", $headers),
                'content' => $json,
                'timeout' => $timeout,
                'ignore_errors' => true,
                'protocol_version' => 1.1,
            ],
        ]);
        $stream = @fopen($url, 'rb', false, $context);
        if($stream === false)
        {
            throw new RuntimeProcessException(
                'process_transport_failed',
                'runtime process bridge is unreachable',
                'unknown'
            );
        }
        $meta = stream_get_meta_data($stream);
        $raw = stream_get_contents($stream, self::MaxBytes);
        fclose($stream);
        if($raw === false || strlen($raw) >= self::MaxBytes)
        {
            throw new RuntimeProcessException('invalid_response', 'process response is too large or unreadable');
        }
        $status = self::status($meta['wrapper_data'] ?? []);
        $data = null;
        if($raw !== '')
        {
            $data = json_decode($raw, true);
            if(json_last_error() !== JSON_ERROR_NONE)
            {
                throw new RuntimeProcessException('invalid_response', 'process bridge returned invalid JSON', '', $status);
            }
        }
        return [$status, $data];
    }

    private static function status(mixed $headers): int
    {
        $status = 0;
        if(is_array($headers))
        {
            foreach($headers as $header)
            {
                if(is_string($header) && preg_match('/^HTTP\/\S+\s+(\d{3})/', $header, $matches))
                {
                    $status = (int)$matches[1];
                }
            }
        }
        return $status;
    }
}
