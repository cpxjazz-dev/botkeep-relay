/* 照片灯箱：点照片放大，可左右切换、点空白或按 Esc 关闭（照片页用）。
   卡片是动态加的（上传的照片），所以每次打开都重新数一遍。 */
(function () {
  var gallery = document.getElementById('gallery');
  var box = document.getElementById('lightbox');
  var img = document.getElementById('lb-img');
  var cap = document.getElementById('lb-cap');
  var prev = document.getElementById('lb-prev');
  var next = document.getElementById('lb-next');
  var close = document.getElementById('lb-close');
  if (!box || !gallery) return;

  function cards() {
    return Array.prototype.slice.call(gallery.querySelectorAll('.photo-card'));
  }
  var current = 0;
  function show(i) {
    var list = cards();
    if (!list.length) return;
    current = (i + list.length) % list.length;
    var photo = list[current].querySelector('img');
    var caption = list[current].querySelector('.cap');
    if (!photo) return;
    // 灯箱看原图：画廊里是 400px 缩略图，点开后用 data-full 里的原图链接。
    img.src = photo.dataset.full || photo.src;
    img.alt = photo.alt || '';
    cap.innerHTML = caption ? caption.innerHTML : '';
    box.classList.add('open');
    document.body.style.overflow = 'hidden';
  }
  function hide() {
    box.classList.remove('open');
    document.body.style.overflow = '';
  }
  // 事件委托：新上传的照片也能点开。
  gallery.addEventListener('click', function (e) {
    var card = e.target.closest('.photo-card');
    if (!card) return;
    var list = cards();
    show(list.indexOf(card));
  });
  prev.addEventListener('click', function (e) { e.stopPropagation(); show(current - 1); });
  next.addEventListener('click', function (e) { e.stopPropagation(); show(current + 1); });
  close.addEventListener('click', function (e) { e.stopPropagation(); hide(); });
  box.addEventListener('click', function (e) {
    if (e.target === box) hide();
  });
  document.addEventListener('keydown', function (e) {
    if (!box.classList.contains('open')) return;
    if (e.key === 'Escape') hide();
    if (e.key === 'ArrowLeft') show(current - 1);
    if (e.key === 'ArrowRight') show(current + 1);
  });
})();
