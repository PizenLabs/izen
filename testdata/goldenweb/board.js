// Task board behaviour for the golden objective fixture.
//
// This file is referenced correctly, so the runtime serves it successfully. It
// exists to prove the observation pass distinguishes a WORKING reference from a
// broken one: a pass that only counted references could not tell them apart.

document.addEventListener("DOMContentLoaded", function () {
  var cards = document.querySelectorAll(".card");
  cards.forEach(function (card) {
    card.dataset.observed = "true";
  });
  document.body.dataset.cardCount = String(cards.length);
});