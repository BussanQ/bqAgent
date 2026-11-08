import type { PromptQuestion, QuestionAnswer } from "./types";

function questionTitle(q: PromptQuestion): string {
  return q.header && q.header !== q.title ? q.header + " · " + q.title : q.title;
}

function questionFields(q: PromptQuestion, request: string) {
  const group = document.createElement("fieldset"), legend = document.createElement("legend");
  group.className = "prompt-question";
  legend.textContent = questionTitle(q) + (q.multi_select ? "（可多选）" : "");
  group.append(legend);
  const fields = (q.options || []).map(option => {
    const label = document.createElement("label"), input = document.createElement("input"), text = document.createElement("span");
    input.type = q.multi_select ? "checkbox" : "radio";
    input.name = request + "/" + q.id;
    input.value = option.id;
    text.textContent = option.label;
    if (option.description) {
      const description = document.createElement("small");
      description.textContent = option.description;
      text.append(description);
    }
    label.append(input, text);
    group.append(label);
    return input;
  });
  const other = document.createElement("textarea");
  other.className = "agent-dialog-field";
  other.rows = 2;
  other.placeholder = fields.length ? q.multi_select ? "补充回答（可选）" : "或填写其他回答" : "填写回答";
  other.setAttribute("aria-label", q.title + " · 填写回答");
  if (!q.multi_select) {
    other.addEventListener("input", () => {
      if (other.value.trim()) fields.forEach(field => { field.checked = false; });
    });
    for (const field of fields) field.addEventListener("change", () => { if (field.checked) other.value = ""; });
  }
  group.append(other);
  function read(): QuestionAnswer {
    const ids = fields.filter(field => field.checked).map(field => field.value), custom = other.value.trim();
    if (!ids.length && !custom) {
      (fields[0] || other).focus();
      throw new Error("请回答「" + q.title + "」");
    }
    return { question_id:q.id, option_ids:ids, ...(custom && { other:custom }) };
  }
  return { group, read };
}

export function questionGroup(questions: PromptQuestion[], request: string) {
  const element = document.createElement("div");
  element.className = "prompt-question-group";
  const fields = questions.map(q => questionFields(q, request));
  element.append(...fields.map(f => f.group));
  return { element, answers: () => fields.map(f => f.read()) };
}

export function questionSummary(questions: PromptQuestion[], answered: boolean): HTMLElement {
  const group = document.createElement("div");
  group.className = "prompt-question-group";
  for (const q of questions) {
    const section = document.createElement("section"), title = document.createElement("strong");
    section.className = "prompt-question";
    title.textContent = questionTitle(q);
    section.append(title);
    if (answered) {
      const result = document.createElement("div"), labels = q.options?.filter(o => o.selected).map(o => o.label) || [];
      result.textContent = labels.length ? "已选：" + labels.join("、") : "已回答";
      section.append(result);
    }
    if (q.options?.length) {
      const details = document.createElement("details"), summary = document.createElement("summary"), list = document.createElement("ul");
      summary.textContent = "查看选项";
      for (const option of q.options) {
        const item = document.createElement("li");
        item.textContent = option.description ? option.label + " · " + option.description : option.label;
        list.append(item);
      }
      details.append(summary,list);
      section.append(details);
    }
    group.append(section);
  }
  return group;
}
