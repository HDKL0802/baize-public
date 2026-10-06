---
name: Office 与 PDF
description: 处理 Word / Excel / PPT / PDF 这类文档：先探测运行环境里有没有可用的解析或生成工具，有就走工具链，没有就如实告知并给出替代方案（Markdown / CSV / HTML）。适用于"读这个 PDF""生成一个 Excel""把这份文档转一下"。
---

# Office 与 PDF

这些格式**不是纯文本**，能不能处理完全取决于运行环境装了什么。
所以第一件事永远是**探环境**，而不是直接假设"能读"。

## 第一步：探环境（必做，不要跳过）

用 `shell_run` 探一次，把结果如实告诉用户：

| 探什么 | 命令（按环境可用性挑一条） |
|---|---|
| 有没有 python3 / python | `python3 --version` 或 `python --version` |
| 有没有对应库 | `python3 -c "import docx, openpyxl, pptx"`、`python3 -c "import pypdf"` |
| 有没有 LibreOffice | `soffice --version` 或 `libreoffice --version` |
| 有没有解压工具 | `unzip -v` |
| 有没有 PDF 取文本 | `pdftotext -v` |

**白泽的 NAS 容器默认是很精简的镜像**（只有 chromium 与中文字体），通常**没有 python、也没有 LibreOffice**。
不要假设有 —— 探出来没有就照下面的「诚实做法」来。

## 各格式怎么做

- **读 `.docx` / `.xlsx` / `.pptx`**：本质是 zip + XML。
  - 有 python：用 `python-docx` / `openpyxl` / `python-pptx`
  - 没有 python：`unzip` 解出来读 XML，能拿到**文字**，拿不到排版与公式结果
- **读 `.pdf`**：`pdftotext` 最好；有 python 用 `pypdf`；两样都没有 → **读不了**，直说
- **生成二进制 Office 文件**：需要 `python-docx` / `openpyxl` / `python-pptx`，
  或用 LibreOffice 从 Markdown/HTML/CSV 转换
- **CSV**：纯文本，`fs_write` 直接写就行（能直接导进 Excel），优先用它交付表格

## 环境不具备时的诚实做法

1. **明确告诉用户**"当前环境没有处理 X 的工具" —— 不要假装打开了、也不要假装生成了。
2. **给替代**：要交付内容就用 `fs_write` 输出 **Markdown / CSV / HTML**（内容一样有用，用户自己粘进 Office 即可）。
3. **要真 Office 文件**：告诉用户需要在环境里装什么（例如 Debian 系 `apt-get install python3-openpyxl`），让用户自己决定要不要装。

## 硬规矩

- **绝不谎报"文件已生成"**：写完必须用 `fs_list` 或 `fs_read` 确认文件真的存在、大小不为 0，再向用户汇报。
- 大表格优先 CSV，不要为了"像 Excel"去硬凑二进制格式。
