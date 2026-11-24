const $ = (id) => document.getElementById(id);
const state = { devices: [], device: '', storage: null, location: '', path: '', next: 0, sequence: 0, listing: null, requests: new Map(), capacity: new Map(), cluster: false, editor: null };
const labels = new Map([['Downloads','下载'],['Documents','文稿'],['Desktop','桌面'],['Pictures','图片'],['Movies','影片'],['Music','音乐'],['Videos','视频']]);
const date = new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'short' });
const clockTime = new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' });
const number = new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 2 });

function element(tag, text, className) {
  const result = document.createElement(tag);
  if (text) result.textContent = text;
  if (className) result.className = className;
  return result;
}

function button(text, className, action) {
  const result = element('button', text, className);
  result.type = 'button';
  result.addEventListener('click', action);
  return result;
}

function bytes(value) {
  const units = ['B','KB','MB','GB','TB','PB'];
  let position = 0;
  while (value >= 1000 && position < units.length - 1) { value /= 1000; position++; }
  return `${number.format(value)} ${units[position]}`;
}

function apiURL(path, values = {}) { return `/api/${path}?${new URLSearchParams(values)}`; }

async function request(path, values, signal) {
  const response = await fetch(apiURL(path, values), { cache: 'no-store', signal });
  const data = await response.json().catch(() => null);
  if (!response.ok || !data) throw new Error(data?.error || '暂时无法读取，请刷新重试。');
  return data;
}

async function putFile(values, body, version) {
  const response = await fetch(apiURL('file', values), {
    method: 'PUT', cache: 'no-store',
    headers: { 'Content-Type': 'text/plain; charset=utf-8', 'If-Match': version, 'X-Device-Files-Action': '1' },
    body,
  });
  const data = await response.json().catch(() => null);
  if (!response.ok || !data) throw new Error(data?.error || '保存失败，请重试。');
  return data;
}

async function deleteRequest(values) {
  const response = await fetch(apiURL('file', values), { method: 'DELETE', cache: 'no-store', headers: { 'X-Device-Files-Action': '1' } });
  const data = await response.json().catch(() => null);
  if (!response.ok || !data) throw new Error(data?.error || '删除失败，请重试。');
  return data;
}

async function uploadRequest(values, file) {
  const response = await fetch(apiURL('upload', values), {
    method: 'POST', cache: 'no-store',
    headers: { 'Content-Type': 'application/octet-stream', 'X-Device-Files-Action': '1' },
    body: file,
  });
  const data = await response.json().catch(() => null);
  if (!response.ok || !data) throw new Error(data?.error || '上传失败，请重试。');
  return data;
}

function storageFor(device) {
  const requests = state.requests;
  if (!requests.has(device)) {
    const pending = request('storage',{device});
    requests.set(device,pending);
    pending.catch(() => {
      if (requests.get(device) === pending) requests.delete(device);
    });
  }
  return requests.get(device);
}

function diskTotals(disks) {
  return disks.reduce((sum, disk) => ({ total:sum.total+disk.total, used:sum.used+disk.used, free:sum.free+disk.free }),
    { total:0, used:0, free:0 });
}

function renderUsage(bar, usage, label) {
  bar.max = usage.total || 1;
  bar.value = usage.used;
  bar.setAttribute('aria-label',`${label} 已使用 ${bytes(usage.used)}，总容量 ${bytes(usage.total)}`);
  bar.classList.toggle('nearly-full',usage.total > 0 && usage.used / usage.total >= .9);
}

function deviceCard({device, disks}) {
  const card = element('article','','device-card');
  const top = element('div','','card-top');
  const label = element('div');
  label.append(element('h3',device.name),element('span',device.address,'secondary'));
  const action = disks.length ? '查看文件' : (device.connector && device.online ? '接入文件' : '查看设备');
  const open = button(action,'button',() => selectDevice(device.id));
  open.setAttribute('aria-label',`${action} · ${device.name}`);
  top.append(label,open);
  card.append(top);
  if (!disks.length) {
    const status = device.online ? '暂未取得存储数据' : '设备离线';
    card.append(element('p',status,'secondary'));
    return card;
  }
  const usage = diskTotals(disks);
  card.append(element('p',`合计 ${bytes(usage.total)} · 已用 ${bytes(usage.used)} · 可用 ${bytes(usage.free)}`,'device-usage'));
  const list = element('ul','','drive-list');
  for (const disk of disks) {
    const row = element('li');
    const heading = element('div','','drive-heading');
    const bar = element('progress');
    renderUsage(bar,disk,`${device.name} ${disk.name}`);
    heading.append(element('strong',disk.name),element('span',bytes(disk.total),'secondary'));
    row.append(heading,bar,element('div',`已用 ${bytes(disk.used)} · 可用 ${bytes(disk.free)}`,'secondary drive-caption'));
    list.append(row);
  }
  card.append(list);
  return card;
}

function renderCapacity(devices) {
  const rows = devices.map((device) => ({device, disks:state.capacity.get(device.id)?.disks || []}));
  const included = rows.filter(({disks}) => disks.length);
  const disks = included.flatMap((row) => row.disks);
  $('overview-status').hidden = !!disks.length;
  $('overview-status').textContent = '正在读取存储使用情况…';
  $('overview-devices').hidden = !rows.length;
  $('device-cards').replaceChildren(...rows.map(deviceCard));
  $('overview-total').hidden = !disks.length;
  if (disks.length) {
    const usage = diskTotals(disks);
    $('summary-scope').textContent = `已统计 ${included.length}/${rows.length} 台设备 · ${disks.length} 块盘`;
    $('summary-total').textContent = bytes(usage.total);
    $('summary-used').textContent = bytes(usage.used);
    $('summary-free').textContent = bytes(usage.free);
    renderUsage($('summary-progress'),usage,'云盘合计');
    $('summary-percent').textContent = `已用 ${number.format(usage.used / (usage.total || 1) * 100)}%`;
  }
}

async function networkStorage() {
  const requests = state.requests;
  const devices = state.devices.filter((device) => device.local || device.connector);
  for (const device of devices) if (!device.online) state.capacity.delete(device.id);
  renderCapacity(devices);
  renderCluster();
  await Promise.all(devices.filter((device) => device.online).map(async (device) => {
    let snapshot;
    try { snapshot = await storageFor(device.id); } catch { /* Keep other devices available. */ }
    if (state.requests !== requests) return;
    if (snapshot) state.capacity.set(device.id,snapshot);
    else state.capacity.delete(device.id);
    renderCapacity(devices);
    renderCluster();
  }));
  if (state.requests === requests && !devices.some((device) => state.capacity.has(device.id))) {
    $('overview-status').textContent = '暂时读不到存储数据，请刷新重试。';
  }
}

function renderCluster() {
  if (!state.cluster || state.device) return;
  const devices = state.devices.filter((device) => device.local || device.connector);
  const host = devices.find((device) => device.local);
  const workspace = state.capacity.get(host?.id)?.locations.find((location) => location.id === 'workspace');
  $('open-workspace').disabled = !workspace;
  $('workspace-status').textContent = workspace ? '写入 Mac mini 的共享外置盘' : '正在连接或外置盘不可用';
  $('cluster-devices').replaceChildren(...devices.map((device) => {
    const card = element('article','','device-card');
    const top = element('div','','card-top');
    top.append(element('h3',device.name),element('span',device.online ? '在线' : '离线','secondary'));
    card.append(top);
    const locations = state.capacity.get(device.id)?.locations.filter((location) => location.id !== 'workspace') || [];
    if (locations.length) {
      const list = element('div','','cluster-locations');
      list.append(...locations.map((location) => button(location.name,'button',() => selectDevice(device.id,false,location.id))));
      card.append(list);
    } else if (device.online) {
      card.append(button('查看设备','button',() => selectDevice(device.id)));
    }
    return card;
  }));
}

function showCluster() {
  ++state.sequence;
  cancelListing();
  state.cluster = true;
  state.device = '';
  state.storage = null;
  $('device-title').textContent = '统一文件库';
  $('device-meta').textContent = '';
  $('notice').replaceChildren();
  $('overview').hidden = true;
  $('connected').hidden = true;
  $('cluster').hidden = false;
  renderDevices();
  renderCluster();
}

function renderDevices() {
  $('home').setAttribute('aria-current',state.device || state.cluster ? 'false' : 'page');
  $('cluster-nav').setAttribute('aria-current',state.cluster ? 'page' : 'false');
  $('devices').replaceChildren(...state.devices.map((device) => {
    const row = button('', 'device', () => { state.cluster = false; selectDevice(device.id); });
    row.setAttribute('aria-current', String(device.id === state.device));
    const name = element('span');
    name.append(element('span', device.name, 'device-name'), element('span', device.address || device.os, 'device-address'));
    const status = element('span', '', device.online ? 'dot online' : 'dot');
    status.setAttribute('aria-label', device.online ? '在线' : '离线');
    row.append(element('span', '', device.os === 'iOS' ? 'device-icon phone' : 'device-icon'), name, status);
    return row;
  }));
}

function connectorInstaller(connectDevice) {
  const panel = element('div');
  const actions = element('div', '', 'setup-actions');
  if (connectDevice.os === 'windows') {
    const download = element('a','下载 Windows 接入工具','button primary');
    download.href = apiURL('installer',{device:connectDevice.id});
    actions.append(download);
    panel.append(actions,element('p','在 Windows 上解压，双击 Install.bat。安装后回到此页点「刷新」。','secondary'));
  } else {
    const picker = element('select');
    picker.setAttribute('aria-label','MacBook 芯片类型');
    for (const [value, text] of [['arm64','Apple 芯片（M 系列）'],['amd64','Intel 芯片']]) {
      const option = element('option',text);
      option.value = value;
      picker.append(option);
    }
    const download = element('a','下载 MacBook 接入工具','button primary');
    const changeLink = () => { download.href = apiURL('installer',{device:connectDevice.id,arch:picker.value}); };
    picker.addEventListener('change',changeLink);
    changeLink();
    actions.append(picker,download);
    panel.append(actions,element('p','在 MacBook 上解压，打开 Install.command。安装后回到此页点「刷新」。','secondary'));
  }
  return panel;
}

function blank(title, description, connectDevice = null) {
  const panel = element('section', '', 'panel');
  panel.append(element('h2',title),element('p',description));
  if (connectDevice) panel.append(connectorInstaller(connectDevice));
  $('notice').replaceChildren(panel);
}

function renderStorage() {
  const { disks, locations } = state.storage;
  const usage = diskTotals(disks);
  $('disk-total').textContent = disks.length ? `合计 ${bytes(usage.total)} · 可用 ${bytes(usage.free)}` : '';
  $('disks').replaceChildren(...disks.map((disk) => {
    const card = element('article','','disk');
    const bar = element('progress');
    renderUsage(bar,disk,disk.name);
    card.append(element('h3',disk.name),element('strong',bytes(disk.total)),bar,
      element('span',`已用 ${bytes(disk.used)} · 可用 ${bytes(disk.free)}`,'secondary'));
    return card;
  }));
  $('locations').replaceChildren(...locations.map((location) => {
    const control = button(location.name,'pill',() => browse(location.id,''));
    control.setAttribute('aria-pressed',String(location.id === state.location));
    return control;
  }));
}

function appendEntries(entries, directory, location, device) {
  const rows = document.createDocumentFragment();
  for (const entry of entries) {
    const filePath = directory ? `${directory}/${entry.name}` : entry.name;
    const row = element('div','','entry');
    row.setAttribute('role','listitem');
    const nameCell = element('div','','entry-name-cell');
    const link = entry.directory
      ? button('', 'entry-name', () => browse(location,filePath))
      : button('', 'entry-name', () => openEditor(device,location,filePath,entry.name));
    const filename = (location === 'home' && !directory) ? labels.get(entry.name) || entry.name : entry.name;
    link.append(element('span','',entry.directory ? 'entry-icon folder' : 'entry-icon'), element('span',filename,'filename'));
    nameCell.append(link);
    if (!entry.directory || location === 'workspace') {
      const download = element('a','下载','entry-download');
      download.href = apiURL('download',{device,location,path:filePath});
      download.setAttribute('aria-label',`下载 ${entry.name}`);
      const remove = button('删除','entry-delete', async () => {
        if (!window.confirm(`确定删除「${entry.name}」？删除后不可恢复。`)) return;
        try {
          await deleteRequest({device,location,path:filePath,kind:entry.directory ? 'folder' : 'file'});
          if (isCurrentFolder(device,location,directory)) browse(location,directory);
        } catch (error) {
          window.alert(error.message);
        }
      });
      remove.setAttribute('aria-label',`删除 ${entry.name}`);
      if (!entry.directory) nameCell.append(download);
      nameCell.append(remove);
    }
    row.append(nameCell,element('span',entry.directory ? '—' : bytes(entry.size),'entry-detail'),element('span',date.format(new Date(entry.modified)),'entry-detail'));
    rows.append(row);
  }
  $('entries').append(rows);
}

function isCurrentFolder(device, location, directory) {
  return state.device === device && state.location === location && state.path === directory;
}

function cancelListing() {
  state.listing?.abort();
  state.listing = null;
}

function requestListing(device, location, path, offset) {
  cancelListing();
  const controller = new AbortController();
  state.listing = controller;
  const pending = request('list',{device,location,path,offset},controller.signal);
  // A prefetched list can fail before storage is ready to consume it.
  pending.catch(() => {});
  return pending;
}

async function browse(location, directory, append = false, prefetched) {
  if (!state.storage?.locations.some((item) => item.id === location)) return;
  const sequence = ++state.sequence;
  const device = state.device;
  state.location = location;
  state.path = directory;
  $('new-folder').hidden = location !== 'workspace';
  if (!append) {
    state.next = 0;
    renderStorage();
    const root = state.storage.locations.find((item) => item.id === location);
    const parts = directory ? directory.split('/') : [];
    const crumb = [button(root.name,'crumb',() => browse(location,''))];
    if (state.cluster) crumb.unshift(button('统一文件库','crumb',showCluster),element('span','/','separator'));
    parts.forEach((part,index) => {
      const name = (location === 'home' && index === 0) ? labels.get(part) || part : part;
      const target = parts.slice(0,index+1).join('/');
      crumb.push(element('span','/','separator'),button(name,'crumb',() => browse(location,target)));
    });
    $('breadcrumbs').replaceChildren(...crumb);
    $('entries').replaceChildren(element('div','正在读取…','empty'));
    $('quicklinks').hidden = true;
    $('listing-footer').hidden = true;
  }
  $('entries').setAttribute('aria-busy','true');
  $('next').disabled = true;
  try {
    const listing = await (prefetched || requestListing(device,location,directory,state.next));
    if (sequence !== state.sequence) return;
    if (!append) $('entries').replaceChildren();
    if (!listing.count) $('entries').append(element('div','这个文件夹是空的。','empty'));
    appendEntries(listing.entries,directory,location,device);
    if (!append && location === 'home' && !directory) {
      const shortcuts = ['Downloads','Desktop','Documents','Pictures'].filter((name) => listing.entries.some((entry) => entry.name === name && entry.directory));
      $('quicklinks').replaceChildren(...shortcuts.map((name) => button(labels.get(name),'button',() => browse(location,name))));
      $('quicklinks').hidden = !shortcuts.length;
    }
    state.next = listing.next;
    $('entry-count').textContent = `${listing.count} 项`;
    $('listing-footer').hidden = !listing.count;
    $('next').hidden = listing.next >= listing.count;
  } catch (error) {
    if (sequence !== state.sequence) return;
    if (!append) $('entries').replaceChildren();
    $('entries').append(element('div',error.message,'empty'));
  } finally {
    if (sequence === state.sequence) {
      $('next').disabled = false;
      $('entries').setAttribute('aria-busy','false');
    }
  }
}

async function openEditor(device, location, filePath, name) {
  const editor = { device, location, path: filePath, name, version: '', writable: false, dirty: false, saving: false };
  state.editor = editor;
  $('editor-title').textContent = name;
  $('editor-meta').textContent = '正在读取…';
  $('editor-status').textContent = '';
  $('editor-message').hidden = false;
  $('editor-message').textContent = '正在读取文件…';
  $('editor-text').hidden = true;
  $('editor-text').value = '';
  $('editor-image').hidden = true;
  $('editor-image').removeAttribute('src');
  $('editor-save').hidden = true;
  $('editor-save').disabled = true;
  $('editor-download').href = apiURL('download',{device,location,path:filePath});
  $('editor').hidden = false;
  document.body.classList.add('editor-open');
  try {
    const details = await request('file',{device,location,path:filePath});
    if (state.editor !== editor) return;
    editor.version = details.version || '';
    editor.writable = !!details.writable;
    if (details.text != null) {
      $('editor-text').value = details.text;
      $('editor-text').hidden = false;
      $('editor-message').hidden = true;
      $('editor-save').hidden = !editor.writable;
      $('editor-save').disabled = !editor.writable;
      $('editor-meta').textContent = editor.writable ? '可编辑 · 保存时会检查冲突' : '只读文件';
      $('editor-text').focus();
    } else if (details.image) {
      $('editor-image').src = apiURL('preview',{device,location,path:filePath});
      $('editor-image').hidden = false;
      $('editor-message').hidden = true;
      $('editor-meta').textContent = '图片预览';
    } else {
      $('editor-message').textContent = editor.writable ? '此文件不是可在网页编辑的纯文本，请下载后编辑。' : '此文件无法在网页中编辑，可下载查看。';
      $('editor-meta').textContent = '不可编辑';
    }
  } catch (error) {
    if (state.editor !== editor) return;
    $('editor-message').textContent = error.message;
    $('editor-meta').textContent = '读取失败';
  }
}

function closeEditor() {
  if (state.editor?.dirty && !window.confirm('有未保存的修改，确定关闭吗？')) return;
  state.editor = null;
  $('editor').hidden = true;
  document.body.classList.remove('editor-open');
}

async function saveEditor() {
  const editor = state.editor;
  if (!editor || editor.saving || !editor.writable) return;
  editor.saving = true;
  $('editor-save').disabled = true;
  $('editor-status').textContent = '正在保存…';
  try {
    const result = await putFile({device:editor.device,location:editor.location,path:editor.path}, $('editor-text').value, editor.version);
    if (state.editor !== editor) return;
    editor.version = result.version;
    editor.dirty = false;
    $('editor-status').textContent = '已保存';
  } catch (error) {
    if (state.editor !== editor) return;
    $('editor-status').textContent = error.message;
  } finally {
    if (state.editor === editor) {
      editor.saving = false;
      $('editor-save').disabled = !editor.writable;
    }
  }
}

async function selectDevice(id, restore = false, preferred = '') {
  const device = state.devices.find((item) => item.id === id);
  if (!device) return;
  const sequence = ++state.sequence;
  cancelListing();
  state.device = id;
  const remembered = restore ? {location:state.location, path:state.path} : {location:preferred, path:''};
  state.storage = null;
  renderDevices();
  $('device-title').textContent = device.name;
  $('device-meta').textContent = `${device.online ? '在线' : '离线'}${device.address ? ` · ${device.address}` : ''}`;
  $('notice').replaceChildren();
  $('overview').hidden = true;
  $('cluster').hidden = true;
  $('connected').hidden = true;
  if (device.os === 'iOS') { blank('iPhone 暂不支持文件浏览','iPhone 的存储和文件目前不在此网页中开放。'); return; }
  if (!device.online) { blank('设备离线','等这台设备重新连上 Tailscale 后再刷新。'); return; }
  if (!device.local && !device.connector) {
    blank('仅显示设备状态','这台设备还没有连接文件服务。');
    return;
  }
  $('notice').append(element('div','正在读取磁盘…','empty'));
  try {
    const cached = !restore && state.capacity.get(id);
    const loadingStorage = cached || storageFor(id);
    const prefetched = !cached && !restore && !preferred ? requestListing(id,'home','',0) : null;
    const storage = await loadingStorage;
    if (sequence !== state.sequence) return;
    const initial = remembered.location ? storage.locations.find((item) => item.id === remembered.location) : storage.locations[0];
    if (!initial) {
      cancelListing();
      blank('文件位置不可用','请返回统一文件库，确认磁盘已连接后再打开。');
      return;
    }
    state.storage = storage;
    $('notice').replaceChildren();
    $('connected').hidden = false;
    $('device-capacity').hidden = state.cluster;
    $('connector-tools').hidden = device.local;
    $('connector-tools').replaceChildren();
    if (!device.local) {
      const upgrade = element('details');
      upgrade.append(element('summary','更新设备接入工具'),connectorInstaller(device));
      $('connector-tools').append(upgrade);
    }
    browse(initial.id,remembered.path,false,initial.id === 'home' ? prefetched : null);
  } catch (error) {
    if (sequence !== state.sequence) return;
    cancelListing();
    if (device.connector) {
      const windows = device.os === 'windows';
      blank(windows ? 'Windows 文件服务未接入' : 'MacBook 文件服务未接入',
        windows ? 'Tailscale 已连接。请在 Windows 本机安装一次接入工具，网页才能读取它的文件和磁盘容量。'
                : 'Tailscale 已连接。请在 MacBook 本机安装一次接入工具，网页才能读取它的文件和磁盘容量。',
        device);
    } else blank('无法读取存储',error.message);
  }
}

function showOverview() {
  ++state.sequence;
  cancelListing();
  state.cluster = false;
  $('cluster').hidden = true;
  state.device = '';
  state.storage = null;
  $('device-title').textContent = '存储总览';
  $('device-meta').textContent = '';
  $('notice').replaceChildren();
  $('connected').hidden = true;
  $('overview').hidden = false;
  renderDevices();
}

async function refresh(reloadFiles = false) {
  if ($('refresh').disabled) return;
  $('refresh').disabled = true;
  try {
    const devices = await request('devices');
    const changed = JSON.stringify(devices) !== JSON.stringify(state.devices);
    const previous = state.devices.find((item) => item.id === state.device);
    state.devices = devices;
    state.requests = new Map();
    const updates = [networkStorage()];
    const selected = state.devices.find((item) => item.id === state.device);
    const selectionChanged = JSON.stringify(selected) !== JSON.stringify(previous);
    if (selected && (reloadFiles || selectionChanged) && !state.editor) {
      updates.push(selectDevice(selected.id,true));
    } else if (!selected && state.device) {
      showOverview();
    } else {
      if (changed) renderDevices();
      if (selected && state.storage) {
        updates.push(storageFor(selected.id).then((storage) => {
          if (state.device === selected.id && state.storage) {
            state.storage = storage;
            renderStorage();
          }
        }).catch(() => {}));
      }
    }
    await Promise.all(updates);
    $('last-update').textContent = `更新于 ${clockTime.format(new Date())}`;
  } catch (error) {
    if (state.device) blank('无法读取设备列表',error.message);
    else {
      $('overview-status').hidden = false;
      $('overview-status').textContent = error.message;
    }
  } finally { $('refresh').disabled = false; }
}

$('refresh').addEventListener('click',() => refresh(true));
$('home').addEventListener('click',showOverview);
$('cluster-nav').addEventListener('click',showCluster);
$('open-workspace').addEventListener('click',() => {
  const host = state.devices.find((device) => device.local);
  if (host) selectDevice(host.id,false,'workspace');
});
$('new-folder').addEventListener('click', async () => {
  const { device, location, path } = state;
  if (location !== 'workspace') return;
  const name = window.prompt('文件夹名称')?.trim();
  if (!name) return;
  if (/[\\/:]/.test(name) || name.startsWith('.')) {
    $('upload-status').textContent = '请输入有效的文件夹名称。';
    return;
  }
  try {
    const response = await fetch(apiURL('mkdir',{device,location,path:path ? `${path}/${name}` : name}),{
      method:'POST', headers:{'X-Device-Files-Action':'1'},
    });
    if (!response.ok) throw new Error((await response.json()).error);
    if (isCurrentFolder(device,location,path)) browse(location,path);
  } catch (error) {
    if (isCurrentFolder(device,location,path)) $('upload-status').textContent = error.message;
  }
});
$('next').addEventListener('click',() => browse(state.location,state.path,true));
$('upload').addEventListener('click',() => $('upload-input').click());
$('upload-input').addEventListener('change', async (event) => {
  const files = Array.from(event.target.files || []);
  event.target.value = '';
  const { device, location, path } = state;
  if (!files.length || !device || !location) return;
  $('upload').disabled = true;
  let uploaded = 0;
  let lastError = '';
  for (const file of files) {
    const target = path ? `${path}/${file.name}` : file.name;
    $('upload-status').textContent = `正在上传 ${file.name}…`;
    try {
      await uploadRequest({device,location,path:target}, file);
      uploaded++;
    } catch (error) {
      lastError = `${file.name}：${error.message}`;
    }
  }
  $('upload-status').textContent = lastError || (uploaded ? `已上传 ${uploaded} 个文件` : '');
  $('upload').disabled = false;
  if (uploaded && isCurrentFolder(device,location,path)) browse(location,path);
});
$('editor-close').addEventListener('click',closeEditor);
$('editor-save').addEventListener('click',saveEditor);
$('editor-text').addEventListener('input',() => {
  if (!state.editor) return;
  state.editor.dirty = true;
  $('editor-status').textContent = '未保存';
});
$('editor-image').addEventListener('error',() => {
  if (!state.editor) return;
  $('editor-image').hidden = true;
  $('editor-message').hidden = false;
  $('editor-message').textContent = '图片预览加载失败，可下载查看。';
});
document.addEventListener('keydown',(event) => {
  if (event.key === 'Escape' && !$('editor').hidden) closeEditor();
});
setInterval(() => { if (!document.hidden) refresh(); },20000);
document.addEventListener('visibilitychange',() => { if (!document.hidden) refresh(); });
refresh();
