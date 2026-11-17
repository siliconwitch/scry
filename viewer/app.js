const element = (id) => document.getElementById(id);

const formatSize = (bytes) => {
  if (bytes < 1024) return bytes + " B";
  if (bytes < 1024 * 1024) return Math.round(bytes / 1024) + " KB";
  return (bytes / 1024 / 1024).toFixed(1) + " MB";
};

let toastTimer;

const showToast = (text) => {
  const toast = element("toast");
  toast.textContent = text;
  toast.hidden = false;

  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (toast.hidden = true), 3000);
};

const copyable = (text) => {
  const span = document.createElement("span");
  span.className = "copy";
  span.textContent = text;

  span.onclick = async () => {
    await navigator.clipboard.writeText(text);
    showToast("Copied " + text);
  };

  return span;
};

const showAddresses = (target, addresses) => {
  target.replaceChildren();

  addresses.forEach((address, index) => {
    if (index > 0) target.append(", ");

    if (!address.name) {
      target.append(copyable(address.email));
      return;
    }

    target.append(copyable(address.name), " <", copyable(address.email), ">");
  });
};

let current;
let shownAttachment = -1;
let renderGeneration = 0;
let pdfEngine;
let pdfDocument;
let pdfSelection;
let officeViewer;

const clearAttachment = () => {
  renderGeneration++;

  element("image-view").hidden = true;
  element("image").removeAttribute("src");
  element("pdf-view").hidden = true;
  element("pdf-view").replaceChildren();
  pdfDocument?.close().catch(() => {});
  pdfDocument = undefined;
  pdfSelection = undefined;
  element("pdf-view").onpointerdown = element("pdf-view").onpointermove = element("pdf-view").onpointerup = element("pdf-view").onclick = null;
  element("media-view").hidden = true;
  element("media").pause();
  element("media").removeAttribute("src");
  element("media").load();
  element("text-view").hidden = true;
  element("office-view").hidden = true;
  officeViewer?.destroy();
  officeViewer = undefined;
  element("office-view").replaceChildren();
};

const showState = (text) => {
  clearAttachment();
  element("state-text").textContent = text;
  element("state").hidden = false;
  element("header").hidden = true;
  element("body").hidden = true;
};

const viewable = () => current.attachments.flatMap((attachment, index) => (attachment.kind ? [index] : []));

const showEmail = () => {
  clearAttachment();
  shownAttachment = -1;
  element("meta").hidden = false;
  element("attachments").hidden = false;
  element("viewer-bar").hidden = true;
  element("body").hidden = false;
};

const showPdf = async (url, generation) => {
  const view = element("pdf-view");
  const resource = (path) => new URL("embedpdf/" + path, location.href).href;

  try {
    const embedpdf = await import(resource("embedpdf.js"));

    pdfEngine ??= embedpdf.localEngine({ worker: resource("embedpdf-worker.js"), wasmUrl: resource("embedpdf.wasm"), encoderWorker: false });

    const response = await fetch(url);

    if (!response.ok) throw new Error("The attachment is no longer available.");

    const pdf = await pdfEngine.open({ kind: "bytes", bytes: await response.arrayBuffer() });

    if (generation !== renderGeneration) {
      pdf.close().catch(() => {});
      return;
    }

    pdfDocument = pdf;

    if (pdf.security.passwordPrompt.state === "required") {
      const note = document.createElement("p");
      note.textContent = "This PDF needs a password. Download it to open it.";
      view.replaceChildren(note);
      return;
    }

    const { pages } = await pdf.pages.list();
    const pageViews = [];

    // A selection runs from where the drag started to where the pointer is, each a page and a character on it.
    const drawSelection = () => {
      for (const pageView of pageViews) pageView.highlights.replaceChildren();

      if (!pdfSelection) return;

      const [start, end] = [pdfSelection.anchor, pdfSelection.focus].sort((a, b) => a.page - b.page || a.char - b.char);
      pdfSelection.ranges = [];

      for (let index = start.page; index <= end.page && index < pageViews.length; index++) {
        const pageView = pageViews[index];
        const first = index === start.page ? start.char : 0;
        const last = index === end.page ? end.char : pageView.text.charCount - 1;

        if (last < first) continue;

        const range = { start: first, count: last - first + 1 };
        pdfSelection.ranges.push({ page: pageView.page, range });

        for (const segment of pageView.text.segments(range)) {
          const box = pageView.transform.pageToPixels(segment.rect);
          const highlight = document.createElement("div");
          highlight.style.left = box.x / pageView.scale + "px";
          highlight.style.top = box.y / pageView.scale + "px";
          highlight.style.width = box.width / pageView.scale + "px";
          highlight.style.height = box.height / pageView.scale + "px";
          pageView.highlights.append(highlight);
        }
      }
    };

    // The character under the pointer, on the page level with it. Between lines and in margins it is null.
    const characterAt = (event) => {
      if (pageViews.length === 0) return null;

      const index = Math.max(0, pageViews.findIndex((pageView) => event.clientY <= pageView.element.getBoundingClientRect().bottom));
      const pageView = pageViews[index];
      const bounds = pageView.element.getBoundingClientRect();
      const point = pageView.transform.pixelsToPage({ x: (event.clientX - bounds.left) * pageView.scale, y: (event.clientY - bounds.top) * pageView.scale });
      const char = pageView.text.charAt(point);

      return char === null ? null : { page: index, char };
    };

    let dragging = false;
    let moved = false;

    view.onpointerdown = (event) => {
      if (event.button !== 0 || event.target.closest(".pdf-links a")) return;

      event.preventDefault();
      view.setPointerCapture(event.pointerId);
      dragging = true;
      moved = false;

      const at = characterAt(event);
      pdfSelection = at ? { anchor: at, focus: at, text: "" } : undefined;
      drawSelection();
    };

    view.onpointermove = (event) => {
      if (!dragging || !pdfSelection) return;

      const at = characterAt(event);

      if (!at || (at.page === pdfSelection.focus.page && at.char === pdfSelection.focus.char)) return;

      moved = true;
      pdfSelection.focus = at;
      drawSelection();
    };

    const readSelection = async () => {
      const selection = pdfSelection;
      const texts = await Promise.all(selection.ranges.map(({ page, range }) => page.text.slice(range)));
      selection.text = texts.join("\n");
    };

    view.onpointerup = () => {
      if (!dragging) return;

      dragging = false;

      if (!moved) {
        pdfSelection = undefined;
        drawSelection();
        return;
      }

      readSelection();
    };

    // A double click selects the word under the pointer, and a triple click its line.
    view.onclick = (event) => {
      if (event.detail !== 2 && event.detail !== 3) return;

      const at = characterAt(event);

      if (!at) return;

      const text = pageViews[at.page].text;
      const range = event.detail === 2 ? text.wordAt(at.char) : text.lineAt(at.char);

      if (!range) return;

      pdfSelection = { anchor: { page: at.page, char: range.start }, focus: { page: at.page, char: range.start + range.count - 1 }, text: "" };
      drawSelection();
      readSelection();
    };

    for (const layout of pages) {
      if (generation !== renderGeneration) return;

      const page = pdf.page(layout.ref);
      const width = Math.min(view.clientWidth - 48, 1100);
      const raster = await page.render.raw({ viewport: { kind: "width", width: Math.round(width * devicePixelRatio) } });
      const text = await page.text.layout();
      const { annotations } = await page.annotations.list();

      if (generation !== renderGeneration) return;

      const scale = raster.width / width;

      const pageElement = document.createElement("div");
      pageElement.className = "pdf-page";
      pageElement.style.width = width + "px";
      pageElement.style.height = raster.height / scale + "px";

      const rows = new Uint8Array(raster.data);
      const pixels = new Uint8ClampedArray(raster.width * raster.height * 4);

      for (let row = 0; row < raster.height; row++) {
        pixels.set(rows.subarray(row * raster.stride, row * raster.stride + raster.width * 4), row * raster.width * 4);
      }

      const canvas = document.createElement("canvas");
      canvas.width = raster.width;
      canvas.height = raster.height;
      canvas.getContext("2d").putImageData(new ImageData(pixels, raster.width, raster.height), 0, 0);

      const highlights = document.createElement("div");
      highlights.className = "pdf-highlights";

      const links = document.createElement("div");
      links.className = "pdf-links";

      pageElement.append(canvas, highlights, links);
      view.append(pageElement);
      pageViews.push({ element: pageElement, page, text, transform: raster.transform, scale, highlights });

      for (const annotation of annotations) {
        if (annotation.subtype !== "link" || !annotation.target) continue;

        const box = raster.transform.pageToPixels(annotation.rect);

        // A link without an href, so Chromium never connects to its host on hover.
        const link = document.createElement("a");
        link.style.left = box.x / scale + "px";
        link.style.top = box.y / scale + "px";
        link.style.width = box.width / scale + "px";
        link.style.height = box.height / scale + "px";

        switch (annotation.target.kind) {
          case "uri":
            link.title = annotation.target.uri;
            link.onclick = () => fetch("open?url=" + encodeURIComponent(annotation.target.uri));
            links.append(link);
            break;

          case "goto": {
            const target = pages.findIndex((candidate) => candidate.ref.objectNumber === annotation.target.destination.page?.objectNumber);

            // A destination the PDF doesn't define stays unclickable.
            if (target === -1) break;

            link.title = "Page " + (target + 1);
            link.onclick = () => pageViews[target]?.element.scrollIntoView({ block: "start" });
            links.append(link);
          }
        }
      }
    }
  } catch {
    if (generation !== renderGeneration) return;

    const note = document.createElement("p");
    note.textContent = "This PDF can't be shown. Download it to open it.";
    view.replaceChildren(note);
  }
};

const showOffice = async (attachment, generation) => {
  const view = element("office-view");

  try {
    const ooxml = await import(new URL("ooxml/" + attachment.kind + ".mjs", location.href).href);

    if (generation !== renderGeneration) return;

    const options = {
      enableTextSelection: true,
      onHyperlinkClick: (target) => {
        if (target.kind === "external") {
          fetch("open?url=" + encodeURIComponent(target.url));
          return;
        }

        if (target.slideIndex !== undefined) officeViewer?.scrollToSlide(target.slideIndex);
      },
    };

    officeViewer = {
      docx: () => new ooxml.DocxScrollViewer(view, options),
      pptx: () => new ooxml.PptxScrollViewer(view, options),
      xlsx: () => new ooxml.XlsxViewer(view, options),
    }[attachment.kind]();

    await officeViewer.load(new URL(attachment.view, location.href).href);
  } catch {
    if (generation !== renderGeneration) return;

    const note = document.createElement("p");
    note.textContent = "This file can't be shown. Download it to open it.";
    view.replaceChildren(note);
  }
};

const showAttachment = (index) => {
  const attachment = current.attachments[index];
  const indexes = viewable();
  clearAttachment();
  const generation = renderGeneration;
  shownAttachment = index;

  element("meta").hidden = true;
  element("attachments").hidden = true;
  element("viewer-bar").hidden = false;
  element("viewer-name").textContent = attachment.name;
  element("viewer-position").textContent = indexes.indexOf(index) + 1 + " of " + indexes.length;
  element("previous").disabled = indexes.length < 2;
  element("next").disabled = indexes.length < 2;
  element("download").href = attachment.url;
  element("download").download = attachment.name;

  element("body").hidden = true;

  switch (attachment.kind) {
    case "image":
      element("image-view").hidden = false;
      element("image").src = attachment.view;
      break;

    case "pdf":
      element("pdf-view").hidden = false;
      showPdf(attachment.view, generation);
      break;

    case "media":
      element("media-view").hidden = false;
      element("media").src = attachment.view;
      break;

    case "text":
      element("text-view").hidden = false;
      element("text-view").contentWindow.location.replace(new URL(attachment.view, location.href).href);
      break;

    default:
      element("office-view").hidden = false;
      showOffice(attachment, generation);
  }
};

const step = (direction) => {
  const indexes = viewable();
  const shown = indexes.indexOf(shownAttachment);

  showAttachment(indexes[(shown + direction + indexes.length) % indexes.length]);
};

// The PDF view draws its own selection, so copying takes the selected text from it.
document.addEventListener("copy", (event) => {
  if (!pdfSelection?.text) return;

  event.clipboardData.setData("text/plain", pdfSelection.text);
  event.preventDefault();
});

element("back").onclick = showEmail;
element("previous").onclick = () => step(-1);
element("next").onclick = () => step(1);

const refresh = async () => {
  const response = await fetch("current");
  const page = await response.json();

  if (page.empty) {
    showState("Select an email in scry");
    return;
  }

  current = page;

  element("subject").textContent = page.subject || "(no subject)";
  document.body.style.setProperty("--account-light", page.accountLight);
  document.body.style.setProperty("--account-dark", page.accountDark);
  showAddresses(element("from"), [page.from]);
  showAddresses(element("to"), page.to || []);
  showAddresses(element("cc"), page.cc || []);
  showAddresses(element("bcc"), page.bcc || []);
  element("cc-row").hidden = !page.cc?.length;
  element("bcc-row").hidden = !page.bcc?.length;
  element("date").textContent = page.date;

  const attachments = element("attachments");
  attachments.replaceChildren();

  page.attachments.forEach((attachment, index) => {
    const size = document.createElement("span");
    size.textContent = formatSize(attachment.size);

    if (attachment.kind) {
      const button = document.createElement("button");
      button.textContent = attachment.name;
      button.append(size);
      button.onclick = () => showAttachment(index);
      attachments.append(button);
      return;
    }

    const link = document.createElement("a");
    link.href = attachment.url;
    link.download = attachment.name;
    link.textContent = attachment.name;
    link.append(size);
    attachments.append(link);
  });

  // Chromium only lets the page close its window while the window has one history entry. Replacing the frame's page adds none.
  element("body").contentWindow.location.replace(new URL(page.body, location.href).href);
  element("state").hidden = true;
  element("header").hidden = false;
  showEmail();
  document.title = (page.subject || "(no subject)") + " - scry";
};

const events = new EventSource("events");

events.onmessage = () => refresh();

events.addEventListener("notice", (event) => showToast(event.data));

events.onerror = async () => {
  try {
    await fetch("current");
  } catch {
    events.close();
    showState("scry has closed");
    document.title = "scry";
    window.close();
  }
};
