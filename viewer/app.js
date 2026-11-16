const element = (id) => document.getElementById(id);

const formatSize = (bytes) => {
  if (bytes < 1024) return bytes + " B";
  if (bytes < 1024 * 1024) return Math.round(bytes / 1024) + " KB";
  return (bytes / 1024 / 1024).toFixed(1) + " MB";
};

let toastTimer;

const copyable = (text) => {
  const span = document.createElement("span");
  span.className = "copy";
  span.textContent = text;

  span.onclick = async () => {
    await navigator.clipboard.writeText(text);

    const toast = element("toast");
    toast.textContent = "Copied " + text;
    toast.hidden = false;

    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => (toast.hidden = true), 1500);
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
  element("state-text").textContent = text;
  element("state").hidden = false;
  element("header").hidden = true;
  element("body").hidden = true;
};

const refresh = async () => {
  const response = await fetch("current");
  const page = await response.json();

  if (page.empty) {
    showState("Select an email in scry");
    return;
  }

  element("subject").textContent = page.subject || "(no subject)";
  document.body.style.setProperty("--account-light", page.accountLight);
  document.body.style.setProperty("--account-dark", page.accountDark);
  showAddresses(element("from"), [page.from]);
  showAddresses(element("to"), page.to);
  element("date").textContent = page.date;

  const attachments = element("attachments");
  attachments.replaceChildren();

  for (const attachment of page.attachments) {
    const link = document.createElement("a");
    link.href = attachment.url;
    link.download = attachment.name;
    link.textContent = attachment.name;

    const size = document.createElement("span");
    size.textContent = formatSize(attachment.size);
    link.append(size);

    attachments.append(link);
  }

  element("body").src = page.body;
  element("state").hidden = true;
  element("header").hidden = false;
  element("body").hidden = false;
  document.title = (page.subject || "(no subject)") + " - scry";
};

const events = new EventSource("events");

events.onmessage = () => refresh();

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
