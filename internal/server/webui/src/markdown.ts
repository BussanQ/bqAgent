import { iconMarkup } from "./icons";

export function escapeHtml(s: unknown): string {
    return String(s == null ? "" : s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;")
      .replace(/'/g, "&#39;");
  }

export function safeHref(url: string): string {
    var value = String(url || "").trim();
    if (/^(https?:|mailto:)/i.test(value) || /^(#|\/(?!\/)|\.\/|\.\.\/)/.test(value)) {
      return value;
    }
    return "";
  }

function applyEmphasis(s: string): string {
    return s
      .replace(/~~([^~]+)~~/g, "<del>$1</del>")
      .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
      .replace(/__([^_]+)__/g, "<strong>$1</strong>")
      .replace(/(^|[^*])\*([^*\n]+)\*/g, "$1<em>$2</em>")
      .replace(/(^|[^_])_([^_\n]+)_/g, "$1<em>$2</em>");
  }

function renderInline(source: unknown): string {
    var tokens: string[] = [];
    function stash(html: string): string {
      var token = "\u0000MD" + tokens.length + "\u0000";
      tokens.push(html);
      return token;
    }

    var text = String(source == null ? "" : source);
    text = text.replace(/(`+)([\s\S]*?)\1/g, function (_, _ticks, code) {
      return stash("<code>" + escapeHtml(code.trim()) + "</code>");
    });
    text = text.replace(/!\[([^\]]*)\]\(([^\s)]+)(?:\s+["']([^"']*)["'])?\)/g,
      function (_, alt, url, title) {
        var src = safeHref(url);
        if (!src || /^mailto:/i.test(src) || /^#/i.test(src)) return alt;
        return stash('<img class="md-image" src="' + escapeHtml(src) + '" alt="' + escapeHtml(alt) + '"' +
          (title ? ' title="' + escapeHtml(title) + '"' : "") + ' loading="lazy" referrerpolicy="no-referrer">');
      });
    text = text.replace(/\[([^\]]+)\]\(([^\s)]+)(?:\s+["']([^"']*)["'])?\)/g,
      function (_, label, url, title) {
        var href = safeHref(url);
        if (!href) return label + " (" + url + ")";
        var titleAttr = title ? ' title="' + escapeHtml(title) + '"' : "";
        var external = /^https?:/i.test(href) ? ' target="_blank" rel="noopener noreferrer"' : "";
        return stash('<a href="' + escapeHtml(href) + '"' + titleAttr + external + '>' +
          applyEmphasis(escapeHtml(label)) + "</a>");
      });
    text = text.replace(/(^|[\s(])(https?:\/\/[^\s<]+)/g, function (_, prefix, url) {
      var clean = url.replace(/[.,;:!?]+$/, "");
      var tail = url.slice(clean.length);
      return prefix + stash('<a href="' + escapeHtml(clean) + '" target="_blank" rel="noopener noreferrer">' +
        escapeHtml(clean) + "</a>") + tail;
    });
    text = applyEmphasis(escapeHtml(text));
    return text.replace(/\u0000MD(\d+)\u0000/g, function (_, index) {
      return tokens[Number(index)] || "";
    });
  }

function isFence(line: string): RegExpMatchArray | null {
    return line.match(/^\s*(`{3,}|~{3,})\s*([a-zA-Z0-9_+.-]*)\s*$/);
  }

function isHorizontalRule(line: string): boolean {
    return /^\s{0,3}((\*\s*){3,}|(-\s*){3,}|(_\s*){3,})\s*$/.test(line);
  }

function isListLine(line: string): RegExpMatchArray | null {
    return line.match(/^\s{0,3}([-+*]|\d+[.)])\s+(.+)$/);
  }

function splitTableRow(line: string): string[] {
    var value = line.trim();
    if (value.charAt(0) === "|") value = value.slice(1);
    if (value.charAt(value.length - 1) === "|") value = value.slice(0, -1);
    var cells = [];
    var current = "";
    var escaped = false;
    for (var i = 0; i < value.length; i++) {
      var ch = value.charAt(i);
      if (escaped) {
        current += ch;
        escaped = false;
      } else if (ch === "\\") {
        escaped = true;
      } else if (ch === "|") {
        cells.push(current.trim());
        current = "";
      } else {
        current += ch;
      }
    }
    cells.push(current.trim());
    return cells;
  }

function tableDelimiter(line: string): string[] | null {
    var cells = splitTableRow(line);
    if (!cells.length) return null;
    var aligns = [];
    for (var i = 0; i < cells.length; i++) {
      var cell = cells[i].replace(/\s/g, "");
      if (!/^:?-{3,}:?$/.test(cell)) return null;
      aligns.push(cell.charAt(0) === ":" && cell.charAt(cell.length - 1) === ":" ? "center" :
        (cell.charAt(cell.length - 1) === ":" ? "right" : "left"));
    }
    return aligns;
  }

function codeBlock(language: string, body: string): string {
    var label = language || "code";
    return '<div class="code-block"><div class="code-head"><span>' + escapeHtml(label) +
      '</span><button class="copy-code" type="button">' + iconMarkup("copy") + '<span>复制</span></button></div><pre><code' +
      (language ? ' class="language-' + escapeHtml(language) + '"' : "") + ">" +
      escapeHtml(body.replace(/\n$/, "")) + "</code></pre></div>";
  }

function renderParagraph(lines: string[]): string {
    var parts = [];
    for (var i = 0; i < lines.length; i++) {
      var hardBreak = /\s{2}$/.test(lines[i]);
      parts.push(renderInline(lines[i].replace(/\s+$/, "")) + (hardBreak ? "<br>" : " "));
    }
    return "<p>" + parts.join("").trim() + "</p>";
  }

function beginsBlock(lines: string[], index: number): boolean {
    var line = lines[index] || "";
    if (!line.trim() || isFence(line) || /^(#{1,6})\s+/.test(line) || /^\s*>/.test(line) ||
        isHorizontalRule(line) || isListLine(line)) return true;
    return index + 1 < lines.length && line.indexOf("|") >= 0 && !!tableDelimiter(lines[index + 1]);
  }

  // Dependency-free safe Markdown renderer. Raw HTML is always escaped. It
  // covers the constructs commonly found in README and generated .md files.
export function renderMarkdown(source: unknown): string {
    var lines = String(source == null ? "" : source).replace(/\r\n?/g, "\n").split("\n");
    var html = [];
    var i = 0;
    while (i < lines.length) {
      var line = lines[i];
      if (!line.trim()) { i++; continue; }

      var fence = isFence(line);
      if (fence) {
        var marker = fence[1];
        var language = fence[2] || "";
        var code = [];
        i++;
        while (i < lines.length && !(new RegExp("^\\s*" + marker.charAt(0) + "{" + marker.length + ",}\\s*$")).test(lines[i])) {
          code.push(lines[i++]);
        }
        if (i < lines.length) i++;
        html.push(codeBlock(language, code.join("\n")));
        continue;
      }

      if (i + 1 < lines.length && /^\s*(=+|-+)\s*$/.test(lines[i + 1]) && lines[i + 1].trim().length >= 3) {
        var setextLevel = lines[i + 1].trim().charAt(0) === "=" ? 1 : 2;
        html.push("<h" + setextLevel + ">" + renderInline(line.trim()) + "</h" + setextLevel + ">");
        i += 2;
        continue;
      }

      var heading = line.match(/^(#{1,6})\s+(.+?)\s*#*$/);
      if (heading) {
        var level = heading[1].length;
        html.push("<h" + level + ">" + renderInline(heading[2]) + "</h" + level + ">");
        i++;
        continue;
      }

      if (isHorizontalRule(line)) {
        html.push("<hr>");
        i++;
        continue;
      }

      if (/^\s*>/.test(line)) {
        var quoted = [];
        while (i < lines.length && (/^\s*>/.test(lines[i]) || !lines[i].trim())) {
          quoted.push(lines[i].replace(/^\s*>\s?/, ""));
          i++;
        }
        html.push("<blockquote>" + renderMarkdown(quoted.join("\n")) + "</blockquote>");
        continue;
      }

      if (i + 1 < lines.length && line.indexOf("|") >= 0) {
        var aligns = tableDelimiter(lines[i + 1]);
        if (aligns) {
          var tableAligns = aligns;
          var headers = splitTableRow(line);
          var rows = [];
          i += 2;
          while (i < lines.length && lines[i].trim() && lines[i].indexOf("|") >= 0) {
            rows.push(splitTableRow(lines[i++]));
          }
          var table = '<div class="table-wrap"><table><thead><tr>';
          headers.forEach(function (cell, column) {
            table += '<th style="text-align:' + (tableAligns[column] || "left") + '">' + renderInline(cell) + "</th>";
          });
          table += "</tr></thead><tbody>";
          rows.forEach(function (row) {
            table += "<tr>";
            headers.forEach(function (_, column) {
              table += '<td style="text-align:' + (tableAligns[column] || "left") + '">' + renderInline(row[column] || "") + "</td>";
            });
            table += "</tr>";
          });
          html.push(table + "</tbody></table></div>");
          continue;
        }
      }

      var list = isListLine(line);
      if (list) {
        var ordered = /^\d/.test(list[1]);
        var tag = ordered ? "ol" : "ul";
        var items = [];
        var hasTasks = false;
        while (i < lines.length) {
          var item = isListLine(lines[i]);
          if (!item || /^\d/.test(item[1]) !== ordered) break;
          var body = item[2];
          var task = body.match(/^\[([ xX])\]\s+(.*)$/);
          if (task) {
            hasTasks = true;
            body = '<input type="checkbox" disabled' + (task[1].toLowerCase() === "x" ? " checked" : "") + ">" + renderInline(task[2]);
          } else {
            body = renderInline(body);
          }
          items.push("<li>" + body + "</li>");
          i++;
        }
        html.push("<" + tag + (hasTasks ? ' class="task-list"' : "") + ">" + items.join("") + "</" + tag + ">");
        continue;
      }

      var paragraph = [line];
      i++;
      while (i < lines.length && !beginsBlock(lines, i)) {
        paragraph.push(lines[i++]);
      }
      html.push(renderParagraph(paragraph));
    }
    return html.join("");
  }
