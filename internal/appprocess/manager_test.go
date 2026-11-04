package appprocess

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vimrus/runtime/internal/config"
)

func TestManagerRejectsCrossApplicationProcessToken(t *testing.T) {
	process := testProcessConfig()
	workerA := newProcessWorker(config.App{ID: "app-a", Path: "/a"}, process, processWorkerOptions{})
	workerB := newProcessWorker(config.App{ID: "app-b", Path: "/b"}, process, processWorkerOptions{})
	manager := &Manager{
		workers: map[string]*processWorker{
			workerA.key(): workerA,
			workerB.key(): workerB,
		},
		callToken: map[string]string{
			"app-a": "token-a",
			"app-b": "token-b",
		},
	}
	server := httptest.NewServer(manager.handler())
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL+BasePath+"/apps/app-b/worker/call", bytes.NewReader([]byte(`{"schema":1}`)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(SchemaHeader, "1")
	request.Header.Set(CallTokenHeader, "token-a")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cross-app token status = %d", response.StatusCode)
	}
}

func TestManagerRunsPHPSDKProcess(t *testing.T) {
	phpBinary := os.Getenv("ZENTAO_TEST_PHP_BINARY")
	if phpBinary == "" {
		var err error
		phpBinary, err = exec.LookPath("php")
		if err != nil {
			if _, statErr := os.Stat("/opt/zentao/php/bin/php"); statErr == nil {
				phpBinary = "/opt/zentao/php/bin/php"
			} else {
				t.Skip("PHP CLI is not available")
			}
		}
	}
	t.Setenv("PHPRC", t.TempDir())
	t.Setenv("PHP_INI_SCAN_DIR", "")
	if strings.HasPrefix(filepath.ToSlash(phpBinary), "/opt/zentao/php/") {
		libraryPath := "/opt/zentao/php/lib"
		if current := os.Getenv("LD_LIBRARY_PATH"); current != "" {
			libraryPath += ":" + current
		}
		t.Setenv("LD_LIBRARY_PATH", libraryPath)
	}
	sdkPath := os.Getenv("ZENTAO_TEST_PROCESS_SDK")
	if sdkPath == "" {
		var err error
		sdkPath, err = filepath.Abs(filepath.Join("..", "..", "php", "runtime-process.php"))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(sdkPath); err != nil {
		t.Fatalf("SDK is missing: %v", err)
	}

	root := t.TempDir()
	appDir := filepath.Join(root, "apps", "zentao")
	dataDir := filepath.Join(root, "data", "apps", "zentao")
	tmpDir := filepath.Join(root, "tmp-local", "zentao")
	scriptDir := filepath.Join(appDir, "service")
	if err := os.MkdirAll(scriptDir, 0o750); err != nil {
		t.Fatal(err)
	}
	script := `<?php
require getenv('ZENTAO_PROCESS_SDK');
if(getenv('ZENTAO_PROCESS_CALL_TOKEN') !== false) throw new RuntimeException('call token leaked');
$worker = RuntimeProcessWorker::fromEnvironment();
$worker->run(function(array $request): array {
    if(($request['method'] ?? '') === 'crash') exit(7);
    if(($request['method'] ?? '') !== 'echo') throw new RuntimeProcessMethodNotFound();
    return ['value' => $request['payload']['value'] ?? null];
});
`
	if err := os.WriteFile(filepath.Join(scriptDir, "worker.php"), []byte(script), 0o640); err != nil {
		t.Fatal(err)
	}
	process := config.DefaultAppProcess()
	process.Name = "worker"
	process.Script = "service/worker.php"
	process.Required = true
	process.ReadyTimeout = 5 * time.Second
	process.RequestTimeout = 3 * time.Second
	process.StopTimeout = 3 * time.Second
	cfg := config.DefaultForRoot(root)
	cfg.Apps = []config.App{{
		ID:        "zentao",
		Path:      "/",
		Dir:       appDir,
		WebRoot:   "www",
		DataDir:   dataDir,
		TmpDir:    tmpDir,
		Enabled:   true,
		Processes: []config.AppProcess{process},
	}}
	manager, err := Prepare(&cfg, Options{
		PHPBinary: phpBinary,
		SDKPath:   sdkPath,
		NodeID:    "test-node",
		PHPArgs:   []string{"-n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	startCtx, startCancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer startCancel()
	if err := manager.Start(startCtx); err != nil {
		t.Fatal(err)
	}
	stopManager := func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		if err := manager.Stop(stopCtx); err != nil {
			t.Errorf("stop manager: %v", err)
		}
	}
	defer stopManager()

	if statuses := manager.Statuses(); len(statuses) != 1 || statuses[0].State != StateReady || statuses[0].PID == 0 {
		t.Fatalf("unexpected process status: %#v", statuses)
	}
	bridgeURL := cfg.Apps[0].Env["ZENTAO_PROCESS_BRIDGE_URL"]
	callToken := cfg.Apps[0].Env["ZENTAO_PROCESS_CALL_TOKEN"]
	body, err := json.Marshal(CallRequest{
		Schema:    SchemaVersion,
		RequestID: "request-a",
		Method:    "echo",
		Payload:   json.RawMessage(`{"value":42}`),
		TimeoutMS: 2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	unauthorized, err := http.NewRequest(http.MethodPost, bridgeURL+"/apps/zentao/worker/call", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Header.Set("Content-Type", "application/json")
	unauthorized.Header.Set(CallTokenHeader, "wrong-token")
	unauthorized.Header.Set(SchemaHeader, "1")
	unauthorizedResponse, err := (&http.Client{Timeout: 5 * time.Second}).Do(unauthorized)
	if err != nil {
		t.Fatal(err)
	}
	_ = unauthorizedResponse.Body.Close()
	if unauthorizedResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d", unauthorizedResponse.StatusCode)
	}

	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, bridgeURL+"/apps/zentao/worker/call", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(CallTokenHeader, callToken)
	request.Header.Set(SchemaHeader, "1")
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("call status = %d: %s", response.StatusCode, data)
	}
	var callResponse CallResponse
	if err := json.Unmarshal(data, &callResponse); err != nil {
		t.Fatal(err)
	}
	if string(callResponse.Result) != `{"value":42}` {
		t.Fatalf("result = %s", callResponse.Result)
	}

	clientScript := filepath.Join(root, "client.php")
	clientCode := `<?php
require getenv('ZENTAO_PROCESS_SDK');
$client = RuntimeProcessClient::forCurrentApp();
echo json_encode($client->call('worker', 'echo', ['value' => 7], 2000), JSON_THROW_ON_ERROR);
`
	if err := os.WriteFile(clientScript, []byte(clientCode), 0o640); err != nil {
		t.Fatal(err)
	}
	client := exec.Command(phpBinary, "-n", clientScript)
	client.Env = append(os.Environ(),
		"ZT_APP_ID=zentao",
		"ZENTAO_PROCESS_APP_ID=zentao",
		"ZENTAO_PROCESS_BRIDGE_URL="+bridgeURL,
		"ZENTAO_PROCESS_CALL_TOKEN="+callToken,
		"ZENTAO_PROCESS_SDK="+sdkPath,
		"ZENTAO_PROCESS_SCHEMA=1",
	)
	clientOutput, err := client.CombinedOutput()
	if err != nil {
		t.Fatalf("PHP process client failed: %v: %s", err, clientOutput)
	}
	if string(bytes.TrimSpace(clientOutput)) != `{"value":7}` {
		t.Fatalf("PHP process client result = %s", clientOutput)
	}

	oldInstance := manager.Statuses()[0].InstanceID
	crashBody, err := json.Marshal(CallRequest{
		Schema:    SchemaVersion,
		RequestID: "crash-request",
		Method:    "crash",
		TimeoutMS: 2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	crashRequest, err := http.NewRequest(http.MethodPost, bridgeURL+"/apps/zentao/worker/call", bytes.NewReader(crashBody))
	if err != nil {
		t.Fatal(err)
	}
	crashRequest.Header.Set("Content-Type", "application/json")
	crashRequest.Header.Set(CallTokenHeader, callToken)
	crashRequest.Header.Set(SchemaHeader, "1")
	crashResponse, err := (&http.Client{Timeout: 5 * time.Second}).Do(crashRequest)
	if err != nil {
		t.Fatal(err)
	}
	crashData, err := io.ReadAll(crashResponse.Body)
	_ = crashResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if crashResponse.StatusCode != http.StatusGatewayTimeout || !bytes.Contains(crashData, []byte(`"outcome_unknown"`)) {
		t.Fatalf("crash response = %d: %s", crashResponse.StatusCode, crashData)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		statuses := manager.Statuses()
		if len(statuses) == 1 && statuses[0].State == StateReady &&
			statuses[0].InstanceID != "" && statuses[0].InstanceID != oldInstance {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("process did not recover after crash: %#v", statuses)
		}
		time.Sleep(20 * time.Millisecond)
	}

	oldInstance = manager.Statuses()[0].InstanceID
	if err := manager.Restart("zentao", "worker"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		statuses := manager.Statuses()
		if len(statuses) == 1 && statuses[0].State == StateReady &&
			statuses[0].InstanceID != "" && statuses[0].InstanceID != oldInstance {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("process did not restart: %#v", statuses)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
