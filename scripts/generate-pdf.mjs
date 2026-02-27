import { readFile, writeFile, mkdir, rm } from "fs/promises";
import { execSync } from "child_process";
import { existsSync } from "fs";
import { join, dirname } from "path";
import { fileURLToPath } from "url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const ROOT = join(__dirname, "..");
const DOCS = join(ROOT, "docs");
const TMP = join(ROOT, ".tmp-pdf");
const INPUT = join(DOCS, "API_DESIGN.md");
const OUTPUT = join(DOCS, "API_DESIGN.pdf");

async function main() {
  if (existsSync(TMP)) await rm(TMP, { recursive: true });
  await mkdir(TMP, { recursive: true });

  let md = await readFile(INPUT, "utf-8");

  const mermaidBlocks = [];
  const mermaidRegex = /```mermaid\n([\s\S]*?)```/g;
  let match;
  while ((match = mermaidRegex.exec(md)) !== null) {
    mermaidBlocks.push({ full: match[0], code: match[1] });
  }

  console.log(`Found ${mermaidBlocks.length} Mermaid diagrams. Rendering...`);

  for (let i = 0; i < mermaidBlocks.length; i++) {
    const block = mermaidBlocks[i];
    const mmdFile = join(TMP, `diagram-${i}.mmd`);
    const svgFile = join(TMP, `diagram-${i}.svg`);

    await writeFile(mmdFile, block.code);

    try {
      execSync(
        `npx mmdc -i "${mmdFile}" -o "${svgFile}" -b transparent --puppeteerConfigFile "${join(TMP, "puppeteer.json")}" 2>&1`,
        { cwd: ROOT, timeout: 30000 }
      );
    } catch {
      try {
        execSync(
          `npx mmdc -i "${mmdFile}" -o "${svgFile}" -b transparent 2>&1`,
          { cwd: ROOT, timeout: 30000 }
        );
      } catch (e2) {
        console.warn(`Warning: Failed to render diagram ${i}, using placeholder. ${e2.message}`);
        await writeFile(
          svgFile,
          `<svg xmlns="http://www.w3.org/2000/svg" width="600" height="100"><text x="10" y="50" font-size="14" fill="#888">[Diagram ${i + 1} — view in Markdown]</text></svg>`
        );
      }
    }

    const svgContent = await readFile(svgFile, "utf-8");
    const svgClean = svgContent
      .replace(/<\?xml[^?]*\?>\s*/g, "")
      .replace(/<!DOCTYPE[^>]*>\s*/g, "");

    md = md.replace(
      block.full,
      `<div class="mermaid-diagram">\n${svgClean}\n</div>`
    );

    console.log(`  Rendered diagram ${i + 1}/${mermaidBlocks.length}`);
  }

  const { marked } = await import("marked");

  marked.setOptions({
    gfm: true,
    breaks: false,
  });

  const htmlBody = marked.parse(md);

  const fullHtml = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>JC Contract Integration — API Design Document</title>
<style>
  @page {
    size: A4;
    margin: 20mm 18mm 20mm 18mm;
  }

  * { box-sizing: border-box; }

  body {
    font-family: "Segoe UI", "Helvetica Neue", Arial, sans-serif;
    font-size: 11pt;
    line-height: 1.55;
    color: #1a1a2e;
    max-width: 100%;
    padding: 0;
    margin: 0;
  }

  h1 {
    font-size: 22pt;
    color: #0f3460;
    border-bottom: 3px solid #0f3460;
    padding-bottom: 8px;
    margin-top: 0;
    page-break-after: avoid;
  }

  h2 {
    font-size: 16pt;
    color: #16213e;
    border-bottom: 1.5px solid #e0e0e0;
    padding-bottom: 5px;
    margin-top: 28px;
    page-break-after: avoid;
  }

  h3 {
    font-size: 13pt;
    color: #1a1a40;
    margin-top: 20px;
    page-break-after: avoid;
  }

  h4 {
    font-size: 11.5pt;
    color: #333;
    margin-top: 16px;
    page-break-after: avoid;
  }

  blockquote {
    border-left: 4px solid #0f3460;
    background: #f0f4ff;
    margin: 12px 0;
    padding: 10px 16px;
    color: #333;
    font-size: 10pt;
    border-radius: 0 4px 4px 0;
  }

  blockquote strong { color: #0f3460; }

  table {
    width: 100%;
    border-collapse: collapse;
    margin: 12px 0;
    font-size: 10pt;
    page-break-inside: auto;
  }

  thead { background: #0f3460; color: #fff; }
  th { padding: 8px 10px; text-align: left; font-weight: 600; }
  td { padding: 7px 10px; border-bottom: 1px solid #e0e0e0; }
  tr:nth-child(even) { background: #f8f9fc; }

  code {
    font-family: "Fira Code", "Cascadia Code", "Consolas", monospace;
    font-size: 9.5pt;
    background: #f0f2f5;
    padding: 1px 5px;
    border-radius: 3px;
    color: #c7254e;
  }

  pre {
    background: #1e1e2e;
    color: #cdd6f4;
    padding: 14px 16px;
    border-radius: 6px;
    overflow-x: auto;
    font-size: 9pt;
    line-height: 1.45;
    page-break-inside: avoid;
    margin: 10px 0;
  }

  pre code {
    background: none;
    color: inherit;
    padding: 0;
    font-size: inherit;
  }

  .mermaid-diagram {
    text-align: center;
    margin: 16px auto;
    page-break-inside: avoid;
    overflow: hidden;
  }

  .mermaid-diagram svg {
    max-width: 100%;
    height: auto;
  }

  hr {
    border: none;
    border-top: 1.5px solid #e0e0e0;
    margin: 24px 0;
  }

  a { color: #0f3460; text-decoration: none; }
  a:hover { text-decoration: underline; }

  ul, ol { padding-left: 24px; }
  li { margin-bottom: 3px; }

  p { margin: 8px 0; }

  em { color: #555; }
</style>
</head>
<body>
${htmlBody}
</body>
</html>`;

  const htmlFile = join(TMP, "api-design.html");
  await writeFile(htmlFile, fullHtml);
  console.log("HTML generated. Converting to PDF...");

  const puppeteer = await import("puppeteer");
  const browser = await puppeteer.launch({
    headless: true,
    args: [
      "--no-sandbox",
      "--disable-setuid-sandbox",
      "--disable-dev-shm-usage",
      "--disable-gpu",
    ],
  });

  const page = await browser.newPage();
  await page.setContent(fullHtml, { waitUntil: "networkidle0", timeout: 30000 });

  await page.pdf({
    path: OUTPUT,
    format: "A4",
    margin: { top: "20mm", right: "18mm", bottom: "20mm", left: "18mm" },
    printBackground: true,
    displayHeaderFooter: true,
    headerTemplate: `
      <div style="width:100%;font-size:8pt;color:#999;padding:0 18mm;display:flex;justify-content:space-between;">
        <span>JC Contract Integration — API Design Document</span>
        <span>aptos-labs/jc-contract-integration</span>
      </div>`,
    footerTemplate: `
      <div style="width:100%;font-size:8pt;color:#999;padding:0 18mm;display:flex;justify-content:space-between;">
        <span>Confidential</span>
        <span>Page <span class="pageNumber"></span> of <span class="totalPages"></span></span>
      </div>`,
  });

  await browser.close();
  console.log(`PDF generated: ${OUTPUT}`);

  await rm(TMP, { recursive: true });
}

main().catch((err) => {
  console.error("Failed to generate PDF:", err);
  process.exit(1);
});
