package clickhouse

import (
	"os"
	"os/exec"
	"testing"
)

func TestPythonLogHelpersBuildSafeCompactQueries(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}

	pythonModule, err := os.ReadFile("python/clickhouse.py")
	if err != nil {
		t.Fatalf("read python/clickhouse.py: %v", err)
	}
	if len(pythonModule) == 0 {
		t.Fatal("python/clickhouse.py is empty")
	}

	script := `
import importlib.util
import sys
import types

pd = types.ModuleType("pandas")
pd.DataFrame = object
sys.modules["pandas"] = pd

ethpandaops = types.ModuleType("ethpandaops")
runtime = types.SimpleNamespace()
ethpandaops._runtime = runtime
sys.modules["ethpandaops"] = ethpandaops

spec = importlib.util.spec_from_file_location("clickhouse", "python/clickhouse.py")
mod = importlib.util.module_from_spec(spec)
sys.modules["clickhouse"] = mod
spec.loader.exec_module(mod)

captured = []

def fake_rows(operation, args):
    captured.append((operation, args))
    sql = args["sql"]
    if "countIf" in sql:
        return [("10", "5", "2", "1", "2026-06-04 11:00:00", "2026-06-04 12:00:00")], [
            "lines",
            "severity_number_lines",
            "severity_text_lines",
            "log_attr_level_lines",
            "first_seen",
            "last_seen",
        ]
    if "UNION ALL" in sql:
        return [("before", "2026-06-04 11:59:59", "n", "h", "", "c.log", "ERROR", "17", "before body", "0")], [
            "context",
            "ts",
            "network",
            "host",
            "service",
            "container",
            "severity",
            "severity_number",
            "body",
            "body_truncated",
        ]
    if "any(substring(clean" in sql:
        return [("c.log", "12", "2026-06-04 11:00:00", "2026-06-04 12:00:00", "sample body")], [
            "value",
            "lines",
            "first_seen",
            "last_seen",
            "sample",
        ]
    return [("2026-06-04 12:00:00", "n", "h", "", "c.log", "ERROR", "17", "body", "1")], [
        "ts",
        "network",
        "host",
        "service",
        "container",
        "severity",
        "severity_number",
        "body",
        "body_truncated",
    ]

def fake_dataframe(operation, args):
    captured.append((operation, args))
    return {"operation": operation, "args": args}

runtime.invoke_tsv_rows = fake_rows
runtime.invoke_tsv_dataframe = fake_dataframe

sources = mod.log_sources()
assert {source["name"] for source in sources} == {"hosted_devnet", "local_kurtosis"}
assert "network" in next(source for source in sources if source["name"] == "hosted_devnet")["fields"]

coverage = mod.log_coverage("hosted_devnet", {"network": "n", "host": "h"})
assert coverage["lines"] == 10
assert coverage["severity_number_coverage"] == 0.5
assert "query" not in coverage
assert coverage["query_omitted"]

errors = mod.log_errors("hosted_devnet", {"network": "n", "host": "h"}, include_sql=True)
assert errors["datasource"] == "clickhouse-raw"
assert errors["rows"][0]["severity_number"] == 17
assert errors["rows"][0]["body_truncated"] is True
assert "query" in errors
assert "ResourceAttributes['network'] = {network:String}" in errors["query"]["sql"]
assert "match(clean, {level_re:String})" in errors["query"]["sql"]
assert errors["query"]["parameters"]["body_chars"] == 240

warns = mod.log_errors("local_kurtosis", {"enclave": "e"}, like_filters={"service": "el-%"}, min_severity="warn", include_sql=True)
assert warns["kind"] == "warn_logs"
assert "SeverityNumber >= 13" in warns["query"]["sql"]
assert "WARN" in warns["query"]["parameters"]["level_re"]

context = mod.log_context("hosted_devnet", {"network": "n", "host": "h"}, "2026-06-04T12:00:00Z", before=1, after=1, include_sql=True)
assert context["rows"][0]["context"] == "before"
assert "UNION ALL" in context["query"]["sql"]
assert "center - INTERVAL 1 HOUR" in context["query"]["sql"]

samples = mod.log_samples("hosted_devnet", "container", {"network": "n", "host": "h"}, include_sql=True)
assert samples["rows"][0]["value"] == "c.log"
assert samples["rows"][0]["lines"] == 12
assert "any(substring(clean" in samples["query"]["sql"]

values = mod.log_values(
    "hosted_devnet",
    "host",
    {"network": "n"},
    like_filters={"host": "lighthouse-%"},
    exclude_filters={"host": "bootnode-1"},
)
values_sql = values["args"]["sql"]
assert "ResourceAttributes['host.name'] AS value" in values_sql
assert "LIKE {host_like:String}" in values_sql
assert "!= {host:String}" in values_sql

try:
    mod.log_errors("hosted_devnet", {"host": "h"})
    raise AssertionError("unscoped hosted log_errors should fail")
except ValueError as exc:
    assert "network" in str(exc)

try:
    mod.log_values("hosted_devnet", "unknown", {"network": "n"})
    raise AssertionError("unknown field should fail")
except ValueError as exc:
    assert "unknown field" in str(exc)
`

	cmd := exec.Command("python3", "-c", script)
	cmd.Dir = "."
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("python log helper test failed: %v\n%s", err, output)
	}
}
