// Deterministic graders for the eval set. Zero dependencies; shared by the
// profiler and its tests. A grader returns 0 or 1; no partial credit, so a
// model's category score is the share of questions it answered right.

function clean(s) {
  return String(s == null ? "" : s)
    .replace(/^\\s+|\\s+$/g, "")
    .replace(/[。.]$/, "")
    .toLowerCase();
}

// grade scores one answer against one question's grader: 1 or 0.
export function grade(question, answer) {
  var g = (question && question.grader) || {};
  var a = String(answer == null ? "" : answer);
  var low = a.toLowerCase();
  if (g.type === "exact") {
    return clean(a) === clean(g.value) ? 1 : 0;
  }
  if (g.type === "contains_any") {
    var vs = g.values || [];
    for (var i = 0; i < vs.length; i++) {
      if (low.indexOf(String(vs[i]).toLowerCase()) !== -1) return 1;
    }
    return 0;
  }
  if (g.type === "contains_all") {
    var vs2 = g.values || [];
    for (var j = 0; j < vs2.length; j++) {
      if (low.indexOf(String(vs2[j]).toLowerCase()) === -1) return 0;
    }
    return 1;
  }
  if (g.type === "regex") {
    var rs = g.values || [];
    for (var k = 0; k < rs.length; k++) {
      try { if (new RegExp(rs[k], "i").test(a)) return 1; } catch (e) {}
    }
    return 0;
  }
  return 0;
}
