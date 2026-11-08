/* 照片灯箱：点拍立得放大，可左右切换、点空白或按 Esc 关闭（照片页用） */
(function () {
  var cards = Array.prototype.slice.call(document.querySelectorAll('.polaroid'));
  var box = document.getElementById('lightbox');
  var img = document.getElementById('lb-img');
  var cap = document.getElementById('lb-cap');
  var prev = document.getElementById('lb-prev');
  var next = document.getElementById('lb-next');
  var close = document.getElementById('lb-close');
  if (!box || !cards.length) return;

  var current = 0;
  function show(i) {
    current = (i + cards.length) % cards.length;
    var photo = cards[current].querySelector('img');
    var caption = cards[current].querySelector('.cap');
    if (!photo) return;
    img.src = photo.src;
    img.alt = photo.alt || '';
    cap.innerHTML = caption ? caption.innerHTML : '';
    box.classList.add('open');
    document.body.style.overflow = 'hidden';
  }
  function hide() {
    box.classList.remove('open');
    document.body.style.overflow = '';
  }
  cards.forEach(function (card, i) {
    card.addEventListener('click', function () { show(i); });
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
