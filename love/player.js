/* 音乐播放器（给她页面用）：进页面就自动播放，营造氛围。
   浏览器会拦未交互的自动播放，所以三层兜底：
   1. audio 标签带 autoplay；
   2. 加载后直接调 play()，被拦就静默失败；
   3. 用户第一次点/碰页面任意处时再试一次（此时浏览器放行）。 */
(function () {
  var audio = document.getElementById('audio');
  var btn = document.getElementById('play-btn');
  var fill = document.getElementById('progress-fill');
  var bar = document.getElementById('progress');
  var cur = document.getElementById('time-cur');
  var total = document.getElementById('time-total');
  var vol = document.getElementById('vol');
  if (!audio || !btn) return;

  function fmt(s) {
    if (!isFinite(s)) return '0:00';
    s = Math.floor(s);
    return Math.floor(s / 60) + ':' + ('0' + (s % 60)).slice(-2);
  }
  function setBtn() { btn.textContent = audio.paused ? '▶' : '⏸'; }

  function tryPlay() {
    var p = audio.play();
    if (p && p.catch) p.catch(function () { /* 浏览器拦了，等用户交互 */ });
  }
  /* 进页面就试一次自动播放 */
  tryPlay();
  /* 第一次交互时再试一次（浏览器放行） */
  function onFirstInteract() {
    if (audio.paused) tryPlay();
    document.removeEventListener('pointerdown', onFirstInteract);
    document.removeEventListener('touchstart', onFirstInteract);
  }
  document.addEventListener('pointerdown', onFirstInteract);
  document.addEventListener('touchstart', onFirstInteract);

  btn.addEventListener('click', function () {
    if (audio.paused) { audio.play(); } else { audio.pause(); }
  });
  audio.addEventListener('play', setBtn);
  audio.addEventListener('pause', setBtn);
  audio.addEventListener('loadedmetadata', function () {
    total.textContent = fmt(audio.duration);
  });
  audio.addEventListener('timeupdate', function () {
    if (audio.duration) {
      fill.style.width = (audio.currentTime / audio.duration * 100) + '%';
    }
    cur.textContent = fmt(audio.currentTime);
  });
  audio.addEventListener('ended', function () {
    audio.currentTime = 0;
    tryPlay();
  });
  bar.addEventListener('click', function (e) {
    var rect = bar.getBoundingClientRect();
    var ratio = (e.clientX - rect.left) / rect.width;
    if (audio.duration) audio.currentTime = ratio * audio.duration;
  });
  if (vol) {
    vol.addEventListener('input', function () {
      audio.volume = vol.value / 100;
    });
    audio.volume = vol.value / 100;
  }
  setBtn();
})();
