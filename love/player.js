/* 音乐播放器（首页用） */
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
    setBtn();
  });
  bar.addEventListener('click', function (e) {
    var rect = bar.getBoundingClientRect();
    var ratio = (e.clientX - rect.left) / rect.width;
    if (audio.duration) audio.currentTime = ratio * audio.duration;
  });
  vol.addEventListener('input', function () {
    audio.volume = vol.value / 100;
  });
  audio.volume = vol.value / 100;
  setBtn();
})();
