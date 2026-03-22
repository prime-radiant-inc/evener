# Adaptive Rejection Sampling (Gilks & Wild, 1992)

ars_validate_inputs <- function(n, f, x_init, domain, log, max_iter) {
  if (!is.numeric(n) || length(n) != 1 || is.na(n) || n <= 0 || n != floor(n)) {
    stop("n must be a positive integer")
  }
  if (!is.function(f)) {
    stop("f must be a function")
  }
  if (!is.numeric(x_init) || length(x_init) < 2 || any(!is.finite(x_init))) {
    stop("x_init must be a numeric vector with at least two finite values")
  }
  if (!is.numeric(domain) || length(domain) != 2 || domain[1] >= domain[2]) {
    stop("domain must be numeric length-2 with lower < upper")
  }
  if (any(x_init <= domain[1] | x_init >= domain[2])) {
    stop("x_init must lie strictly inside domain")
  }
  if (!is.logical(log) || length(log) != 1) {
    stop("log must be TRUE/FALSE")
  }
  if (!is.numeric(max_iter) || length(max_iter) != 1 || max_iter <= 0) {
    stop("max_iter must be positive")
  }
}

ars_eval_logf <- function(f, x, log) {
  y <- f(x)
  if (length(y) != length(x)) {
    stop("f must be vectorized and return same length as input")
  }
  if (!log) {
    if (any(y <= 0 | !is.finite(y))) {
      stop("f returned non-positive or non-finite values")
    }
    y <- log(y)
  } else {
    if (any(!is.finite(y))) {
      stop("f returned non-finite log values")
    }
  }
  y
}

ars_eval_logf_prime <- function(f_log, x) {
  h <- 1e-5
  (f_log(x + h) - f_log(x - h)) / (2 * h)
}

ars_check_log_concavity <- function(x, logf, tol = 1e-6) {
  if (length(x) < 3) return(TRUE)
  for (i in 2:(length(x) - 1)) {
    x1 <- x[i - 1]; x2 <- x[i]; x3 <- x[i + 1]
    if (x3 == x1) next
    l1 <- logf[i - 1]; l2 <- logf[i]; l3 <- logf[i + 1]
    t <- (x2 - x1) / (x3 - x1)
    if (l2 < (1 - t) * l1 + t * l3 - tol) return(FALSE)
  }
  TRUE
}

ars_build_upper_hull <- function(x, h, hprime) {
  k <- length(x)
  z <- numeric(k + 1)
  z[1] <- -Inf
  z[k + 1] <- Inf
  for (i in 1:(k - 1)) {
    num <- h[i + 1] - h[i] - x[i + 1] * hprime[i + 1] + x[i] * hprime[i]
    den <- hprime[i] - hprime[i + 1]
    if (den == 0) {
      z[i + 1] <- (x[i] + x[i + 1]) / 2
    } else {
      z[i + 1] <- num / den
    }
  }
  z
}

ars_segment_integral <- function(zl, zr, x, h, hprime) {
  a <- hprime
  b <- h - hprime * x
  if (abs(a) < 1e-12) {
    return(exp(b) * (zr - zl))
  }
  exp(b) * (exp(a * zr) - exp(a * zl)) / a
}

ars_sample_from_hull <- function(x, h, hprime, z) {
  k <- length(x)
  areas <- numeric(k)
  for (i in 1:k) {
    areas[i] <- ars_segment_integral(z[i], z[i + 1], x[i], h[i], hprime[i])
  }
  cum <- cumsum(areas)
  total <- cum[k]
  u <- runif(1) * total
  idx <- which(cum >= u)[1]
  zl <- z[idx]; zr <- z[idx + 1]
  a <- hprime[idx]
  b <- h[idx] - hprime[idx] * x[idx]
  if (abs(a) < 1e-12) {
    xnew <- zl + runif(1) * (zr - zl)
  } else {
    c0 <- exp(a * zl)
    c1 <- exp(a * zr)
    r <- runif(1)
    xnew <- (log(c0 + r * (c1 - c0))) / a
  }
  list(x = xnew, upper_log = a * xnew + b, idx = idx)
}

ars_lower_hull <- function(x, h, xnew) {
  if (xnew <= min(x) || xnew >= max(x)) return(-Inf)
  j <- max(which(x < xnew))
  x1 <- x[j]; x2 <- x[j + 1]
  t <- (xnew - x1) / (x2 - x1)
  (1 - t) * h[j] + t * h[j + 1]
}

ars <- function(n, f, x_init, domain = c(-Inf, Inf), log = FALSE, max_iter = 100000) {
  ars_validate_inputs(n, f, x_init, domain, log, max_iter)
  x_init <- sort(unique(x_init))
  f_log <- function(x) ars_eval_logf(f, x, log)
  h <- f_log(x_init)
  if (!ars_check_log_concavity(x_init, h)) {
    stop("Initial points do not satisfy log-concavity")
  }
  hprime <- ars_eval_logf_prime(f_log, x_init)
  samples <- numeric(n)
  xs <- x_init
  hs <- h
  hps <- hprime
  count <- 0
  iter <- 0
  while (count < n) {
    iter <- iter + 1
    if (iter > max_iter) stop("max_iter exceeded")
    z <- ars_build_upper_hull(xs, hs, hps)
    draw <- ars_sample_from_hull(xs, hs, hps, z)
    xnew <- draw$x
    if (xnew <= domain[1] || xnew >= domain[2]) next
    hnew <- f_log(xnew)
    if (!is.finite(hnew)) stop("Log-density returned non-finite at sampled point")
    lnew <- ars_lower_hull(xs, hs, xnew)
    u <- runif(1)
    if (log(u) <= hnew - draw$upper_log) {
      count <- count + 1
      samples[count] <- xnew
    } else if (log(u) <= lnew - draw$upper_log) {
      count <- count + 1
      samples[count] <- xnew
    }
    xs <- sort(c(xs, xnew))
    hs <- f_log(xs)
    if (!ars_check_log_concavity(xs, hs)) {
      stop("Non-log-concave density detected during sampling")
    }
    hps <- ars_eval_logf_prime(f_log, xs)
  }
  samples
}

ars_summary <- function(x) {
  list(mean = mean(x), sd = sd(x), n = length(x))
}

ars_test_case <- function(name, x, mean_true, sd_true, tol_mean, tol_sd) {
  m <- mean(x)
  s <- sd(x)
  pass <- (abs(m - mean_true) <= tol_mean) && (abs(s - sd_true) <= tol_sd)
  status <- if (pass) "PASS" else "FAIL"
  cat(sprintf("%s: %s mean=%.4f sd=%.4f\n", name, status, m, s))
  pass
}

test <- function() {
  set.seed(123)
  all_pass <- TRUE
  n <- 2000

  normal_logf <- function(x) dnorm(x, log = TRUE)
  xs_norm <- ars(n, normal_logf, x_init = c(-2, 0, 2), log = TRUE)
  write.table(xs_norm, file = "/app/normal_samples.txt", row.names = FALSE, col.names = FALSE)
  pass1 <- ars_test_case("NORMAL_TEST", xs_norm, 0, 1, 0.1, 0.1)
  all_pass <- all_pass && pass1

  expo_logf <- function(x) {
    ifelse(x >= 0, dexp(x, rate = 1, log = TRUE), -Inf)
  }
  xs_exp <- ars(n, expo_logf, x_init = c(0.1, 1, 3), domain = c(0, Inf), log = TRUE)
  write.table(xs_exp, file = "/app/exponential_samples.txt", row.names = FALSE, col.names = FALSE)
  pass2 <- ars_test_case("EXPONENTIAL_TEST", xs_exp, 1, 1, 0.1, 0.1)
  all_pass <- all_pass && pass2

  if (all_pass) {
    cat("ALL_TESTS: PASS\n")
  } else {
    cat("ALL_TESTS: FAIL\n")
  }
  invisible(all_pass)
}
