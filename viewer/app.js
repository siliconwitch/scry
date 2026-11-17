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

const showState = (text) => {
  renderGeneration++;
  element("state-text").textContent = text;
  element("state").hidden = false;
  element("header").hidden = true;
  element("body").hidden = true;
  element("image-view").hidden = true;
  element("pdf-view").hidden = true;
};

let current;
let shownAttachment = -1;
let renderGeneration = 0;
let pdfjs;

const viewable = () => current.attachments.flatMap((attachment, index) => (attachment.kind ? [index] : []));

const showEmail = () => {
  renderGeneration++;
  shownAttachment = -1;
  element("meta").hidden = false;
  element("attachments").hidden = false;
  element("viewer-bar").hidden = true;
  element("image-view").hidden = true;
  element("image").removeAttribute("src");
  element("pdf-view").hidden = true;
  element("pdf-view").replaceChildren();
  element("body").hidden = false;
};

const showPdf = async (url, generation) => {
  const view = element("pdf-view");
  const resource = (path) => new URL("pdfjs/" + path, location.href).href;

  try {
    pdfjs ??= await import(resource("pdf.min.mjs"));
    pdfjs.GlobalWorkerOptions.workerSrc = resource("pdf.worker.min.mjs");

    const response = await fetch(url);

    if (!response.ok) throw new Error("The attachment is no longer available.");

    const task = pdfjs.getDocument({
      data: new Uint8Array(await response.arrayBuffer()),
      cMapUrl: resource("cmaps/"),
      standardFontDataUrl: resource("standard_fonts/"),
      wasmUrl: resource("wasm/"),
      iccUrl: resource("iccs/"),
    });

    const pdf = await task.promise;

    const pages = [];

    try {
      for (let number = 1; number <= pdf.numPages; number++) {
        if (generation !== renderGeneration) return;

        const pdfPage = await pdf.getPage(number);
        const width = Math.min(view.clientWidth - 48, 1100);
        const viewport = pdfPage.getViewport({ scale: width / pdfPage.getViewport({ scale: 1 }).width });

        const pageElement = document.createElement("div");
        pageElement.className = "pdf-page";
        pageElement.style.width = Math.floor(viewport.width) + "px";
        pageElement.style.height = Math.floor(viewport.height) + "px";
        pageElement.style.setProperty("--total-scale-factor", viewport.scale);

        const canvas = document.createElement("canvas");
        canvas.width = Math.floor(viewport.width * devicePixelRatio);
        canvas.height = Math.floor(viewport.height * devicePixelRatio);

        const text = document.createElement("div");
        text.className = "textLayer";

        const links = document.createElement("div");
        links.className = "pdf-links";

        pageElement.append(canvas, text, links);
        view.append(pageElement);
        pages.push(pageElement);

        await pdfPage.render({ canvas, viewport, transform: [devicePixelRatio, 0, 0, devicePixelRatio, 0, 0] }).promise;
        await new pdfjs.TextLayer({ textContentSource: pdfPage.streamTextContent(), container: text, viewport }).render();

        for (const annotation of await pdfPage.getAnnotations({ intent: "display" })) {
          if (annotation.subtype !== "Link" || (!annotation.url && !annotation.dest)) continue;

          // A link without an href, so Chromium never connects to its host on hover.
          const link = document.createElement("a");
          const [x1, y1] = viewport.convertToViewportPoint(annotation.rect[0], annotation.rect[1]);
          const [x2, y2] = viewport.convertToViewportPoint(annotation.rect[2], annotation.rect[3]);
          link.style.left = Math.min(x1, x2) + "px";
          link.style.top = Math.min(y1, y2) + "px";
          link.style.width = Math.abs(x2 - x1) + "px";
          link.style.height = Math.abs(y2 - y1) + "px";

          if (annotation.url) {
            link.title = annotation.url;
            link.onclick = () => fetch("open?url=" + encodeURIComponent(annotation.url));
            links.append(link);
            continue;
          }

          try {
            const destination = typeof annotation.dest === "string" ? await pdf.getDestination(annotation.dest) : annotation.dest;
            const target = typeof destination[0] === "object" ? await pdf.getPageIndex(destination[0]) : destination[0];

            link.title = "Page " + (target + 1);
            link.onclick = () => pages[target]?.scrollIntoView({ block: "start" });
            links.append(link);
          } catch {
            // A destination the PDF doesn't define stays unclickable.
          }
        }
      }
    } finally {
      task.destroy();
    }
  } catch (error) {
    if (generation !== renderGeneration) return;

    const note = document.createElement("p");
    note.textContent =
      error?.name === "PasswordException"
        ? "This PDF needs a password. Download it to open it."
        : "This PDF can't be shown. Download it to open it.";
    view.replaceChildren(note);
  }
};

const showAttachment = (index) => {
  const attachment = current.attachments[index];
  const indexes = viewable();
  const generation = ++renderGeneration;
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
  element("image-view").hidden = attachment.kind !== "image";
  element("pdf-view").hidden = attachment.kind !== "pdf";
  element("pdf-view").replaceChildren();

  if (attachment.kind === "image") {
    element("image").src = attachment.view;
    return;
  }

  element("image").removeAttribute("src");
  showPdf(attachment.view, generation);
};

const step = (direction) => {
  const indexes = viewable();
  const shown = indexes.indexOf(shownAttachment);

  showAttachment(indexes[(shown + direction + indexes.length) % indexes.length]);
};

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
