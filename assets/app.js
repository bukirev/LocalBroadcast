'use strict';
const $ = (id) => document.getElementById(id);
const labels = {master:'Главный OBS',presenter:'Презентер',camera:'Камера','camera-view':'Камера + просмотр',viewer:'Просмотр',custom:'Пользовательский'};
const titles = {overview:['Обзор','Состояние системы и активные потоки'],nodes:['Устройства','Компьютеры и источники захвата'],routes:['Маршрутизация','Управление направлением медиапотоков'],sync:['Синхронизация','Задержка видео и звука камер'],obs:['OBS Studio','Связь с эфирной сценой'],updates:['Обновления','Проверка, загрузка и установка новой версии'],settings:['Настройки','Роли, видеозахват, сеть и интеграции'],logs:['Диагностика','Состояния процессов и журнал событий']};
let current = null;
let activeTab = 'overview';
let settingsDirty = false;
let saving = false;
let syncDirty = false;
let updateChannelDirty=false;
let restarting = false;
let toastTimer;
function el(tag, cls, content) { const n = document.createElement(tag); if (cls) n.className=cls; if(content!==undefined) n.textContent=String(content); return n; }
function clear(n) { while(n.firstChild) n.removeChild(n.firstChild); }
function line(label,value) { const p=el('div','kv');p.append(el('span','',label),el('strong','',value));return p; }
function toast(msg,bad=false) {let t=$('toast');t.textContent=msg;t.classList.toggle('error',bad);t.hidden=false;clearTimeout(toastTimer);toastTimer=setTimeout(()=>t.hidden=true,5500);}
async function get(path){const r=await fetch(path,{cache:'no-store'});if(!r.ok) throw Error(await r.text());return r.json();}
async function post(path,data){const r=await fetch(path,{method:'POST',headers:{'Content-Type':'application/json','X-LB-UI':'1'},body:JSON.stringify(data)});if(!r.ok) throw Error((await r.text()).trim());return r.json();}
function tab(name){if(!titles[name])return;activeTab=name;for(const b of document.querySelectorAll('.nav'))b.classList.toggle('active',b.dataset.tab===name);for(const s of document.querySelectorAll('.tab'))s.hidden=s.id!=='tab-'+name;$('page-title').textContent=titles[name][0];$('page-subtitle').textContent=titles[name][1];}
function flag(name){return current?.config?.[name];}
function activity(icon,title,desc,status){const row=el('div','activity-item');row.append(el('div','activity-icon',icon));const info=el('div');info.append(el('div','activity-title',title),el('div','activity-desc',desc));row.append(info,el('div','activity-indicator',status));return row;}
function renderOverview(state){
 const c=state.config;const master=state.system==='master';const online=state.peers?.filter(p=>Date.now()-Date.parse(p.lastSeen)<8000)||[];
 const agent=state.agent||{};
 $('metric-role').textContent=labels[c.mode]||c.mode;
 $('metric-online').textContent=master?String(online.length)+' ПК':agent.connected?'В сети':'Нет связи';
 $('metric-online-foot').textContent=master?'Клиентов подключено':agent.connected?'Главный OBS найден':'Ожидание главного ПК';
 const media=state.media||{};const streamCount=master?online.reduce((n,p)=>n+(p.sendScreen?1:0)+(p.sendCamera?1:0),0):['screen','camera'].filter(x=>media[x]&&typeof media[x]==='object').length;
 $('metric-streams').textContent=String(streamCount);$('metric-streams-foot').textContent=master?'Ожидаемых входов OBS':'Локальных исходящих потоков';
 $('my-name').textContent=c.name;$('my-ip').textContent=state.localIP;
 $('master-label').textContent=master?'Это главный ПК':agent.master||state.discovered||'Не найден';
 $('ffmpeg-label').textContent=state.ffmpegFound?'Установлен':'Не найден';
 $('ffplay-label').textContent=state.ffplayFound?'Установлен':'Не найден';
 const list=$('activity-list');clear(list);
 if(master){list.append(activity('▣','Координатор','Принимает регистрации и распределяет адреса','Работает'),activity('◫','OBS Studio',state.obs?.ready?'Источники синхронизированы':(state.obs?.error||'Проверка подключения к OBS'),state.obs?.ready?'Готов':'Не готов'))}
 else {
  list.append(activity('◉','Центральное управление',agent.connected?'Профиль и маршруты получены':'Ожидание соединения',agent.connected?'Связь есть':'Ожидание'));
  if(media.screen&&typeof media.screen==='object')list.append(activity('▣','Экран презентера','Получателей: '+media.screen.targets,media.screen.running?'Кодирует':'Запуск'));
  if(media.camera&&typeof media.camera==='object')list.append(activity('◉','Камера','Получателей: '+media.camera.targets,media.camera.running?'Кодирует':'Запуск'));
  if(media.preview)list.append(activity('▤','Предпросмотр','Получение экрана презентера через FFplay','Активен'));
  if(streamCount===0&&!media.preview)list.append(activity('◌','Медиа','Исходящие/входящие потоки пока не активны','Ожидание'));
 }
 $('stream-tag').textContent=master?('Источников: '+streamCount):(streamCount?'Активно':'Ожидание');
 $('master-peers-panel').hidden=!master;$('client-help-panel').hidden=master;
 $('peer-count').textContent=online.length+' онлайн';
 renderPeers($('overview-peers'),state.peers||[],true);
}
function renderPeers(target,peers,compact){clear(target);
 if(!peers||peers.length===0){target.append(el('p','muted small','Пока нет зарегистрированных устройств. Запустите приложение на остальных ПК.'));return;}
 for(const p of peers){const online=Date.now()-Date.parse(p.lastSeen)<8000;const row=el('div','peer-row');row.append(el('div','peer-icon',p.sendScreen?'▣':p.sendCamera?'◉':'▤'));
  const middle=el('div');middle.append(el('div','peer-title',p.name));middle.append(el('div','peer-meta',`${labels[p.mode]||p.mode} · ${p.ip}${p.sendCamera?' · UDP '+p.cameraPort:''}`));
  if(!compact){middle.append(el('div','peer-meta',`ID ${p.id} · ${p.version||'?'} · ${p.mediaStatus||'idle'}`));}
  row.append(middle);row.append(el('div','peer-status'+(online?'':' offline'),online?'● Подключён':'● Не в сети'));target.append(row);
 }
}
function renderRoutes(routes){const root=$('routes-list');clear(root);
 if(!routes||routes.length===0){root.append(el('p','muted small','Потоки появятся после подключения презентера или камер.'));return;}
 for(const route of routes){const row=el('div','route-row');row.append(el('div','',route.source),el('div','',route.target));const check=el('input');check.type='checkbox';check.checked=route.enabled;check.setAttribute('aria-label',`Передача ${route.source} на ${route.target}`);
 check.addEventListener('change',async()=>{check.disabled=true;try{await post('/api/route',{sourceId:route.sourceId,targetId:route.targetId,enabled:check.checked});toast('Маршрут обновлён.');await refresh();}catch(e){check.checked=!check.checked;toast(e.message,true)}finally{check.disabled=false}});
 row.append(check);root.append(row);
 }
}
function renderSync(adjustments){
 const root=$('sync-list');clear(root);
 if(syncDirty&&activeTab==='sync')return;
 const entries=Object.values(adjustments);
 if(!entries.length){root.append(el('p','muted small','Потоки появятся после подключения презентера и камер.'));return}
 for(const a of entries){
  const row=el('div','sync-row');const label=el('div');label.append(el('strong','',a.name),el('div','peer-meta',`Автозадержка: видео ${a.renderDelayMs} мс · аудио ${a.audioOffsetMs} мс`));
  const rtt=el('div','muted small',`${a.rttMs} мс`);
  const video=el('input');video.type='number';video.min='-2000';video.max='10000';video.title='Дополнительная задержка видео, мс';video.value=current.config.syncOffsets?.[a.sourceId]?.videoMs??0;
  const audio=el('input');audio.type='number';audio.min='-950';audio.max='10000';audio.title='Дополнительная задержка аудио, мс';audio.value=current.config.syncOffsets?.[a.sourceId]?.audioMs??0;
  video.addEventListener('input',()=>{syncDirty=true});audio.addEventListener('input',()=>{syncDirty=true});
  const save=el('button','button tiny secondary','Сохранить калибровку');
  save.addEventListener('click',async()=>{save.disabled=true;try{await post('/api/sync',{sourceId:a.sourceId,videoMs:Number(video.value),audioMs:Number(audio.value)});toast('Калибровка сохранена. OBS обновится автоматически.');syncDirty=false;await refresh(true)}catch(e){toast(e.message,true)}finally{save.disabled=false}});
  row.append(label,rtt,video,audio,save);root.append(row)
 }
}
function renderOBS(peers){const root=$('obs-camera-ports');clear(root);
 for(const p of peers||[]){if(!p.sendCamera)continue;const n=el('div','kv');n.append(el('span','',p.name),el('code','',`udp://0.0.0.0:${p.cameraPort}`));root.append(n)}
}
const fields = ['name','mode','secret','masterIp','sendScreen','sendCamera','watchPresenter','autoStart','launchAtLogin','screenFps','screenBitrateK','cameraFps','cameraBitrateK','cameraWidth','cameraHeight','cameraDevice','screenMonitor','screenAudioEndpoint','screenAudioDevice','cameraAudioDevice','screenAudioMode','transport','srtLatencyMs','srtAdaptive','syncEnabled','encoder','ffmpegPath','ffplayPath','obsUrl','obsPassword','obsScene','obsExePath','autoLaunchObs','obsAutoSync'];
const checks = new Set(['sendScreen','sendCamera','watchPresenter','autoStart','launchAtLogin','autoLaunchObs','obsAutoSync','srtAdaptive','syncEnabled']);
const numeric = new Set(['screenFps','screenBitrateK','cameraFps','cameraBitrateK','cameraWidth','cameraHeight','srtLatencyMs']);
function populateForm(cfg){for(const k of fields){const input=$('f-'+k);if(!input)continue;if(checks.has(k))input.checked=!!cfg[k];else {if(['screenMonitor','screenAudioEndpoint'].includes(k))ensureDeviceOption(input,cfg[k]||'');if(k==='screenFps')ensureFPSOption(cfg.screenFps);input.value=cfg[k]??'';}}
 $('f-screenResolution').value=(cfg.screenWidth||1920)+'x'+(cfg.screenHeight||1080);
 if(cfg.screenFpsMode==='adaptive')$('f-screenFps').value='adaptive';
 settingsDirty=false;updateVisibleFields();}
function ensureFPSOption(fps){const input=$('f-screenFps');if(fps&&!Array.from(input.options).some(o=>o.value===String(fps))){const o=document.createElement('option');o.value=String(fps);o.textContent=fps+' (сохранённое значение)';input.append(o)}}
function collectForm(){const cfg=structuredClone(current.config);for(const k of fields){const input=$('f-'+k);if(!input)continue;cfg[k]=checks.has(k)?input.checked:numeric.has(k)?Number(input.value):input.value.trim();}const size=$('f-screenResolution').value.split('x').map(Number);cfg.screenWidth=size[0];cfg.screenHeight=size[1];
 cfg.screenFpsMode=$('f-screenFps').value==='adaptive'?'adaptive':'fixed';if(cfg.screenFpsMode==='adaptive')cfg.screenFps=30;
 return cfg;}
function updateVisibleFields(){
 const mode=$('f-mode').value;
 const screen=mode==='presenter'||(mode==='custom'&&$('f-sendScreen').checked);
 const camera=['camera','camera-view'].includes(mode)||(mode==='custom'&&$('f-sendCamera').checked);
 for(const panel of document.querySelectorAll('.screen-setting'))panel.hidden=!screen;
 for(const panel of document.querySelectorAll('.camera-setting'))panel.hidden=!camera;
 $('media-settings-panel').hidden=!(screen||camera);
 $('obs-settings-panel').hidden=mode!=='master';
 $('media-tools-panel').hidden=mode==='master';
 $('network-media-settings').hidden=mode!=='master';
 $('screen-audio-wasapi').hidden=$('f-screenAudioMode').value!=='wasapi'||!screen;
 $('screen-audio-dshow').hidden=$('f-screenAudioMode').value!=='dshow'||!screen;
}
function updateModeFlags(mode){const defaults={master:[false,false,false],presenter:[true,false,false],camera:[false,true,false],'camera-view':[false,true,true],viewer:[false,false,true]};
 if(!defaults[mode])return;const arr=defaults[mode];['sendScreen','sendCamera','watchPresenter'].forEach((key,i)=>$('f-'+key).checked=arr[i]);}
async function saveSettings(){saving=true;const btn=$('save-config');btn.disabled=true;
 try{const cfg=collectForm();const response=await post('/api/config',cfg);settingsDirty=false;toast('Настройки сохранены'+(response.restartRequired?' — требуется перезапуск':''));if(response.restartRequired)$('restart-notice').hidden=false;await refresh(true)}catch(e){toast(e.message,true)}finally{saving=false;btn.disabled=false;}
}
async function restartApp(){
 if(restarting||saving)return;
 if(settingsDirty){toast('Сначала сохраните настройки.',true);return;}
 restarting=true;const button=$('restart-app');button.disabled=true;
 const oldInstance=current.instanceId;
 $('restart-status').textContent='Перезапуск программы… Ожидаем подключения.';
 try{
  await post('/api/action',{action:'restart'});
  const deadline=Date.now()+45000;
  while(Date.now()<deadline){
   await new Promise(resolve=>setTimeout(resolve,750));
   try{
    const response=await fetch('/api/state',{cache:'no-store',signal:AbortSignal.timeout(2000)});
    if(!response.ok)continue;
    const state=await response.json();
    if(state.instanceId===oldInstance)continue;
    restarting=false;render(state);toast('Программа перезапущена. Настройки применены.');return;
   }catch(e){/* The local server is temporarily unavailable during restart. */}
  }
  throw Error('Программа не подключилась за 45 секунд. Проверьте запуск LocalBroadcast.exe.');
 }catch(e){toast(e.message,true)}finally{
  restarting=false;button.disabled=false;
  $('restart-status').textContent='Настройки сохранены. Для смены главного режима нужен перезапуск программы. Передача потоков временно прервётся.';
 }
}
function render(state){current=state;
 $('version-label').textContent='Версия '+state.version;
 renderUpdates(state.updates);
 const isMaster=state.system==='master';
 for(const el of document.querySelectorAll('.master-only'))el.hidden=!isMaster;
 if(!isMaster&&['routes','obs','sync'].includes(activeTab))tab('overview');
 $('mode-pill').textContent=isMaster?'● РЕЖИМ КООРДИНАТОРА':'● РЕЖИМ КЛИЕНТА';
 const connected=isMaster||state.agent?.connected;
 $('top-status').textContent=connected?'● Система работает':'● Нет подключения';
 $('network-label').textContent=connected?'Система в сети':'Нет подключения';
 $('network-led').className='dot'+(connected?'':' warn');
 $('restart-notice').hidden=!state.restartRequired;
 renderOverview(state);
 renderPeers($('nodes-list'),isMaster?(state.peers||[]):[{id:state.config.nodeId,name:state.config.name,mode:state.config.mode,ip:state.localIP,version:state.version,sendScreen:state.config.sendScreen,sendCamera:state.config.sendCamera,lastSeen:new Date().toISOString(),mediaStatus:state.agent?.connected?'online':'waiting'}],false);
 renderRoutes(state.routes||[]);renderOBS(state.peers||[]);renderSync(state.sync||{});
 $('route-transport').textContent=(state.config.transport==='srt'?'SRT + LOCAL UDP':'UDP UNICAST');
 $('logs-output').textContent=(state.logs||[]).join('\n')||'Журнал пока пуст';
 const video=state.media?.screen;
 $('screen-output-status').textContent=video&&video.running&&video.width?`Профиль передачи: ${video.width} × ${video.height}, ${video.fpsMode==='adaptive'?'адаптивный 15–30 FPS':video.targetFPS+' FPS'}.`:'';
 const encoders=[state.media?.screen,state.media?.camera].filter(x=>x&&typeof x==='object'&&x.encoder);
 $('encoder-status').textContent=encoders.map(x=>x.encoder!==x.requestedEncoder?`Выбран ${x.requestedEncoder}; используется CPU — x264 после ошибки запуска.`:`Используется ${x.encoder}.`).join(' ');

 if(!settingsDirty&&!saving)populateForm(state.config);
}
async function refresh(force=false){if(restarting||(document.hidden&&!force))return;try{const data=await get('/api/state');if(!restarting)render(data)}catch(e){if(!current)toast('Нет связи с приложением: '+e.message,true)}}
function init(){for(const b of document.querySelectorAll('.nav'))b.addEventListener('click',()=>tab(b.dataset.tab));
 $('goto-settings').addEventListener('click',()=>tab('settings'));
 $('toggle-secret').addEventListener('click',()=>{const v=$('f-secret');v.type=v.type==='password'?'text':'password';$('toggle-secret').textContent=v.type==='password'?'Показать':'Скрыть';});
 $('f-mode').addEventListener('change',e=>{updateModeFlags(e.target.value);settingsDirty=true;updateVisibleFields();});
 for(const k of fields){const input=$('f-'+k);if(input)input.addEventListener('input',()=>{settingsDirty=true;if(['sendScreen','sendCamera','screenAudioMode'].includes(k))updateVisibleFields();})}
 $('f-screenResolution').addEventListener('change',()=>{settingsDirty=true});
 $('save-config').addEventListener('click',saveSettings);
 $('restart-app').addEventListener('click',restartApp);
 initUpdates();
 $('detect-devices').addEventListener('click',loadCaptureDevices);
 $('refresh-capture-devices').addEventListener('click',loadCaptureDevices);
 $('launch-obs').addEventListener('click',async()=>{try{await post('/api/action',{action:'open-obs'});toast('Команда запуска OBS отправлена')}catch(e){toast('OBS: '+e.message,true)}});
 $('sync-obs').addEventListener('click',async()=>{const b=$('sync-obs');b.disabled=true;try{await post('/api/action',{action:'obs-sync'});toast('Источники OBS синхронизированы.')}catch(e){toast('OBS: '+e.message,true)}finally{b.disabled=false}});
 $('copy-logs').addEventListener('click',async()=>{try{await navigator.clipboard.writeText($('logs-output').textContent);toast('Журнал скопирован')}catch(e){toast('Не удалось скопировать журнал',true)}});
 tab('overview');
 const updateClock=()=>{$('clock').textContent=new Date().toLocaleString('ru-RU',{hour:'2-digit',minute:'2-digit',day:'numeric',month:'short'})};updateClock();setInterval(updateClock,10000);
 refresh().then(loadCaptureDevices);setInterval(refresh,2500);
}
document.addEventListener('DOMContentLoaded',init);

function renderUpdates(u){
 if(!u)return;
 const busy=['checking','downloading','installing'].includes(u.phase);
 $('update-current').textContent=u.installed;
 if(!updateChannelDirty&&document.activeElement!==$('update-channel'))$('update-channel').value=u.channel;
 $('update-channel').disabled=busy;
 $('update-check').disabled=busy;
 $('update-download').hidden=!u.release||!['available','error'].includes(u.phase);
 $('update-cancel').hidden=!['checking','downloading'].includes(u.phase);
 $('update-status').textContent=u.message+(u.phase==='downloading'&&u.release?` ${ (u.received/1048576).toFixed(1)} / ${(u.release.size/1048576).toFixed(1)} МБ`:'');
 $('update-progress').hidden=u.phase!=='downloading';
 $('update-progress').value=u.release?100*u.received/u.release.size:0;
 $('update-release').textContent=u.release?`Версия ${u.release.version} · ${(u.release.size/1048576).toFixed(1)} МБ`:'';
 $('update-notes').textContent=u.release?.notes||'';
 $('update-source').href=u.release?.url||'https://github.com/bukirev/LocalBroadcast/releases';
 $('update-install-area').hidden=u.phase!=='prepared';
 $('update-exe').textContent=u.executable;
}
function initUpdates(){
 const action=async(name,data={})=>{try{renderUpdates(await post('/api/updates/'+name,data));return true}catch(e){toast(e.message,true);return false}};
 $('update-channel').addEventListener('change',()=>{updateChannelDirty=true});
 $('update-check').addEventListener('click',async()=>{if(await action('check',{channel:$('update-channel').value}))updateChannelDirty=false;});
 $('update-download').addEventListener('click',()=>action('download'));
 $('update-cancel').addEventListener('click',()=>action('cancel'));
 $('update-install').addEventListener('click',async()=>{
  if(settingsDirty||syncDirty){toast('Сначала сохраните изменённые настройки и калибровку.',true);return}
  if(!confirm('Установить новую версию и перезапустить Local Broadcast?'))return;
  const old=current?.instanceId;const node=current?.config?.nodeId;
  $('update-install').disabled=true;
  try{
   renderUpdates(await post('/api/updates/install',{interrupt:$('update-interrupt').checked}));
   const deadline=Date.now()+90000;
   while(Date.now()<deadline){
    await new Promise(resolve=>setTimeout(resolve,1000));
    let state;try{state=await get('/api/state')}catch(e){continue}
    render(state);
    if(state.instanceId!==old&&state.readyForWork&&state.config.nodeId===node){toast('Программа перезапущена. Версия '+state.version);return}
    if(state.instanceId===old&&state.updates?.phase==='error')throw Error(state.updates.message);
   }
   throw Error('Перезапуск не подтверждён. Проверьте окно обновлятора и журнал .lb-update.');
  }catch(e){toast(e.message,true)}finally{$('update-install').disabled=false}
 });
}

function ensureDeviceOption(select,value){
 if(value && !Array.from(select.options).some(o=>o.value===value)){
  const o=document.createElement('option');o.value=value;o.textContent='Недоступно: '+value;select.append(o);
 }
}
function fillDeviceOptions(id,items,defaultLabel){
 const input=$(id);const selected=input.value;
 input.replaceChildren();const first=document.createElement('option');first.value='';first.textContent=defaultLabel;input.append(first);
 for(const item of items){const o=document.createElement('option');o.value=item.id;o.textContent=item.name;input.append(o)}
 ensureDeviceOption(input,selected);input.value=selected;
}
async function loadCaptureDevices(){
 const buttons=[$('detect-devices'),$('refresh-capture-devices')];if(buttons.some(b=>b.disabled))return;
 buttons.forEach(b=>b.disabled=true);$('capture-device-status').textContent='Поиск экранов и аудиоустройств…';
 try{
  const d=await post('/api/devices',{});const outputs=d.audioOutputs||[];const displays=d.displays||[];
  const def=outputs.find(x=>x.default);
  fillDeviceOptions('f-screenMonitor',displays,'Весь рабочий стол — все экраны');
  fillDeviceOptions('f-screenAudioEndpoint',outputs,'Устройство Windows по умолчанию'+(def?' — '+def.name:''));
  $('capture-dshow-audio').replaceChildren();
  for(const match of (d.output||'').matchAll(/"([^"\n]+)" \(audio\)/g)){const o=document.createElement('option');o.value=match[1];$('capture-dshow-audio').append(o)}
  $('device-output').textContent=d.output||'Устройства DirectShow не обнаружены';
  $('capture-device-status').textContent=`Экранов: ${displays.length}; аудиовыходов: ${outputs.length}. `+(d.warnings||[]).join(' ');
 }catch(e){$('capture-device-status').textContent='Не удалось обновить устройства: '+e.message;}
 finally{buttons.forEach(b=>b.disabled=false)}
}
