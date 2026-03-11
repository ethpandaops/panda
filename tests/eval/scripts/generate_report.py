#!/usr/bin/env python3
"""Generate evaluation reports from test results."""

from __future__ import annotations

import argparse
import json
from datetime import datetime
from pathlib import Path

from rich.console import Console
from rich.table import Table


console = Console()


def generate_markdown_report(
    results_file: Path,
    output_file: Path,
    model: str,
) -> None:
    """Generate a Markdown report from test results.

    Args:
        results_file: Path to JSON results file.
        output_file: Path to output Markdown file.
        model: Model used for evaluation.
    """
    with open(results_file) as f:
        results = json.load(f)

    # Calculate summary statistics
    total_tests = len(results.get("tests", []))
    passed = sum(1 for t in results.get("tests", []) if t.get("passed", False))
    failed = total_tests - passed
    total_cost = sum(t.get("cost_usd", 0) for t in results.get("tests", []))
    total_duration = sum(t.get("duration_ms", 0) for t in results.get("tests", []))

    # Generate Markdown
    md_lines = [
        f"# ethpandaops-panda Evaluation Report",
        "",
        f"**Date:** {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}",
        f"**Model:** {model}",
        "",
        "## Summary",
        "",
        f"| Metric | Value |",
        f"|--------|-------|",
        f"| Total Tests | {total_tests} |",
        f"| Passed | {passed} |",
        f"| Failed | {failed} |",
        f"| Pass Rate | {passed / total_tests * 100:.1f}% |",
        f"| Total Cost | ${total_cost:.6f} |",
        f"| Total Duration | {total_duration / 1000:.1f}s |",
        "",
        "## Test Results",
        "",
    ]

    # Add individual test results
    for test in results.get("tests", []):
        status = "✅" if test.get("passed", False) else "❌"
        md_lines.append(f"### {status} {test.get('id', 'Unknown')}")
        md_lines.append("")

        if test.get("description"):
            md_lines.append(f"**Description:** {test['description']}")
            md_lines.append("")

        md_lines.append(f"- **Cost:** ${test.get('cost_usd', 0):.6f}")
        md_lines.append(f"- **Duration:** {test.get('duration_ms', 0)}ms")
        md_lines.append(f"- **Tokens:** {test.get('input_tokens', 0)} in / {test.get('output_tokens', 0)} out")

        if test.get("tools_used"):
            md_lines.append(f"- **Tools:** {', '.join(test['tools_used'])}")

        if test.get("metrics"):
            md_lines.append("")
            md_lines.append("**Metrics:**")
            md_lines.append("")
            md_lines.append("| Metric | Score | Threshold | Passed |")
            md_lines.append("|--------|-------|-----------|--------|")
            for metric in test["metrics"]:
                status = "✅" if metric.get("passed", False) else "❌"
                md_lines.append(
                    f"| {metric.get('name', 'Unknown')} | "
                    f"{metric.get('score', 0):.2f} | "
                    f"{metric.get('threshold', 0):.2f} | "
                    f"{status} |"
                )

        if test.get("error"):
            md_lines.append("")
            md_lines.append(f"**Error:** {test['error']}")

        md_lines.append("")

    # Write output
    with open(output_file, "w") as f:
        f.write("\n".join(md_lines))

    console.print(f"[green]Report generated: {output_file}[/green]")


def generate_html_report(
    results_file: Path,
    output_file: Path,
    model: str,
) -> None:
    """Generate an HTML report from test results.

    Args:
        results_file: Path to JSON results file.
        output_file: Path to output HTML file.
        model: Model used for evaluation.
    """
    with open(results_file) as f:
        results = json.load(f)

    # Calculate summary statistics
    total_tests = len(results.get("tests", []))
    passed = sum(1 for t in results.get("tests", []) if t.get("passed", False))
    failed = total_tests - passed
    total_cost = sum(t.get("cost_usd", 0) for t in results.get("tests", []))

    html = f"""<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>ethpandaops-panda Evaluation Report</title>
    <style>
        body {{
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
            max-width: 1200px;
            margin: 0 auto;
            padding: 20px;
            background: #f5f5f5;
        }}
        .header {{
            background: #1a1a2e;
            color: white;
            padding: 20px;
            border-radius: 8px;
            margin-bottom: 20px;
        }}
        .summary {{
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
            gap: 15px;
            margin-bottom: 20px;
        }}
        .summary-card {{
            background: white;
            padding: 20px;
            border-radius: 8px;
            box-shadow: 0 2px 4px rgba(0,0,0,0.1);
        }}
        .summary-card h3 {{
            margin: 0 0 10px 0;
            color: #666;
            font-size: 14px;
        }}
        .summary-card .value {{
            font-size: 24px;
            font-weight: bold;
        }}
        .test-card {{
            background: white;
            padding: 20px;
            border-radius: 8px;
            margin-bottom: 15px;
            box-shadow: 0 2px 4px rgba(0,0,0,0.1);
        }}
        .test-card.passed {{
            border-left: 4px solid #4caf50;
        }}
        .test-card.failed {{
            border-left: 4px solid #f44336;
        }}
        .test-header {{
            display: flex;
            justify-content: space-between;
            align-items: center;
            margin-bottom: 10px;
        }}
        .test-title {{
            font-weight: bold;
            font-size: 16px;
        }}
        .test-status {{
            padding: 4px 12px;
            border-radius: 4px;
            font-size: 12px;
            font-weight: bold;
        }}
        .test-status.passed {{
            background: #e8f5e9;
            color: #2e7d32;
        }}
        .test-status.failed {{
            background: #ffebee;
            color: #c62828;
        }}
        .metrics-table {{
            width: 100%;
            border-collapse: collapse;
            margin-top: 15px;
        }}
        .metrics-table th, .metrics-table td {{
            padding: 8px 12px;
            text-align: left;
            border-bottom: 1px solid #eee;
        }}
        .metrics-table th {{
            background: #f5f5f5;
        }}
    </style>
</head>
<body>
    <div class="header">
        <h1>ethpandaops-panda Evaluation Report</h1>
        <p>Model: {model} | Date: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}</p>
    </div>

    <div class="summary">
        <div class="summary-card">
            <h3>Total Tests</h3>
            <div class="value">{total_tests}</div>
        </div>
        <div class="summary-card">
            <h3>Passed</h3>
            <div class="value" style="color: #4caf50">{passed}</div>
        </div>
        <div class="summary-card">
            <h3>Failed</h3>
            <div class="value" style="color: #f44336">{failed}</div>
        </div>
        <div class="summary-card">
            <h3>Pass Rate</h3>
            <div class="value">{passed / total_tests * 100:.1f}%</div>
        </div>
        <div class="summary-card">
            <h3>Total Cost</h3>
            <div class="value">${total_cost:.4f}</div>
        </div>
    </div>

    <h2>Test Results</h2>
"""

    for test in results.get("tests", []):
        status = "passed" if test.get("passed", False) else "failed"
        status_text = "PASSED" if test.get("passed", False) else "FAILED"

        html += f"""
    <div class="test-card {status}">
        <div class="test-header">
            <span class="test-title">{test.get('id', 'Unknown')}</span>
            <span class="test-status {status}">{status_text}</span>
        </div>
        <p>{test.get('description', '')}</p>
        <p>Cost: ${test.get('cost_usd', 0):.6f} | Duration: {test.get('duration_ms', 0)}ms</p>
"""

        if test.get("metrics"):
            html += """
        <table class="metrics-table">
            <tr>
                <th>Metric</th>
                <th>Score</th>
                <th>Threshold</th>
                <th>Status</th>
            </tr>
"""
            for metric in test["metrics"]:
                m_status = "✅" if metric.get("passed", False) else "❌"
                html += f"""
            <tr>
                <td>{metric.get('name', 'Unknown')}</td>
                <td>{metric.get('score', 0):.2f}</td>
                <td>{metric.get('threshold', 0):.2f}</td>
                <td>{m_status}</td>
            </tr>
"""
            html += """
        </table>
"""

        html += """
    </div>
"""

    html += """
</body>
</html>
"""

    with open(output_file, "w") as f:
        f.write(html)

    console.print(f"[green]Report generated: {output_file}[/green]")


def main() -> None:
    """Main entry point."""
    parser = argparse.ArgumentParser(description="Generate evaluation reports")
    parser.add_argument(
        "results_file",
        type=Path,
        help="Path to JSON results file",
    )
    parser.add_argument(
        "--output",
        "-o",
        type=Path,
        help="Output file path (default: report.md or report.html)",
    )
    parser.add_argument(
        "--format",
        "-f",
        choices=["markdown", "html"],
        default="markdown",
        help="Output format (default: markdown)",
    )
    parser.add_argument(
        "--model",
        default="claude-sonnet-4-5",
        help="Model used for evaluation",
    )

    args = parser.parse_args()

    if not args.results_file.exists():
        console.print(f"[red]Results file not found: {args.results_file}[/red]")
        return

    output_file = args.output
    if not output_file:
        ext = "md" if args.format == "markdown" else "html"
        output_file = Path(f"report.{ext}")

    if args.format == "markdown":
        generate_markdown_report(args.results_file, output_file, args.model)
    else:
        generate_html_report(args.results_file, output_file, args.model)


if __name__ == "__main__":
    main()
