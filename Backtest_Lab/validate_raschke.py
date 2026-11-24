"""Diagnóstico de raschke - ¿edge real o backtest mintiendo?"""
import os, time, warnings
import numpy as np, pandas as pd, yfinance as yf
warnings.filterwarnings("ignore")
CACHE_DIR = "data_cache"; os.makedirs(CACHE_DIR, exist_ok=True)
BARS_PER_HOUR = {"1m":60,"5m":12,"15m":4,"30m":2,"1h":1}

def download(symbol, interval="1d", start=None, end=None):
    key = f"{symbol.replace('=','_')}_{interval}_{start or 'max'}_{end or 'now'}"
    cache = os.path.join(CACHE_DIR, key + ".csv")
    if os.path.exists(cache): return pd.read_csv(cache, index_col=0, parse_dates=True)
    print(f"  [descargando] {symbol} {interval} {start or ''}->{end or ''}...", end=" ")
    kw = dict(interval=interval, progress=False, auto_adjust=False, threads=False)
    df = yf.download(symbol, start=start, end=end, **kw) if start else yf.download(symbol, period="max", **kw)
    if df is None or df.empty: print("vacio"); return pd.DataFrame()
    if isinstance(df.columns, pd.MultiIndex): df.columns = df.columns.get_level_values(0)
    df.columns = [c.lower() for c in df.columns]
    df = df[["open","high","low","close","volume"]].dropna()
    df.to_csv(cache); print(f"{len(df)} velas"); return df

def compute_ib(df, tf, skip_bars=0):
    df = df.copy()
    if not isinstance(df.index, pd.DatetimeIndex):
        df.index = pd.to_datetime(df.index, errors="coerce", utc=True)
    if getattr(df.index, "tz", None) is not None:
        df.index = df.index.tz_convert(None)
    df = df[df.index.notna()]
    df["date"] = df.index.date
    if tf == "1d":
        df["ib_high"] = df["high"].shift(1); df["ib_low"] = df["low"].shift(1)
        df["pos_in_day"] = 1
    else:
        n = BARS_PER_HOUR[tf]; highs, lows, pos = [], [], []
        for date, grp in df.groupby("date", sort=False):
            ih, il = grp.head(n)["high"].max(), grp.head(n)["low"].min()
            for k in range(len(grp)): highs.append(ih); lows.append(il); pos.append(k)
        df["ib_high"], df["ib_low"], df["pos_in_day"] = highs, lows, pos
    df["ib_mid"] = (df["ib_high"]+df["ib_low"])/2
    df["skip"] = df["pos_in_day"] < skip_bars
    return df

def signals(df, tf, skip, direction):
    df = compute_ib(df, tf, skip); out = []
    for i in range(len(df)):
        r = df.iloc[i]
        if r["skip"] or pd.isna(r["ib_high"]) or pd.isna(r["ib_low"]): continue
        p, ih, il, im = r["close"], r["ib_high"], r["ib_low"], r["ib_mid"]
        if direction in ("both","long") and p >= ih*0.999:
            out.append((i,"long",ih,im,ih+(ih-il)*0.5))
        elif direction in ("both","short") and p <= il*1.001:
            out.append((i,"short",il,im,il-(ih-il)*0.5))
    return out

def simulate(df, sig, exit_slip, max_bars):
    i, d, entry, sl, tp = sig
    si = 0.01; so = 0.02 if exit_slip else 0.0
    ef = entry + (si if d=="long" else -si)
    for j in range(i+1, min(i+1+max_bars, len(df))):
        b = df.iloc[j]
        if d=="long":
            if b["low"] <= sl: return sl - so
            if b["high"] >= tp: return tp - so
        else:
            if b["high"] >= sl: return sl + so
            if b["low"] <= tp: return tp + so
    j = min(i+max_bars, len(df)-1)
    return df["close"].iloc[j] + (so if d=="long" else -so)

def stats(df, sigs, exit_slip=True, max_bars=2):
    if len(sigs) < 3: return None
    trades = []
    for s in sigs:
        px = simulate(df, s, exit_slip, max_bars)
        trades.append((px - s[2]) if s[1]=="long" else (s[2] - px))
    arr = np.array(trades)
    r = arr / df["close"].mean()
    sr = r.mean()/r.std()*np.sqrt(252) if r.std()>0 else 0
    return {"n": len(arr), "wr": (arr>0).mean(), "sharpe": sr, "ret": arr.sum()/df["close"].mean()*100}

def run(name, df, tf, skip=0, direction="both", slip=True, max_bars=2):
    if df.empty: print(f"  {name:50s} sin datos"); return
    s = stats(df, signals(df, tf, skip, direction), slip, max_bars)
    if s is None: print(f"  {name:50s} sin señales"); return
    print(f"  {name:50s} N={s['n']:5d} WR={s['wr']*100:5.1f}% Sharpe={s['sharpe']:6.2f} ret%={s['ret']:7.2f}")

def main():
    print("="*82); print("  VALIDACIÓN DE RASCHKE - ¿Edge real o backtest mintiendo?"); print("="*82)

    print("\n[GRUPO 1: Reproducir y corregir bugs - QQQ 15m]")
    df = download("QQQ", "15m")
    run("1a. Original (look-ahead, sin exit slip)", df, "15m", skip=0, slip=False)
    run("1b. Sin look-ahead (skip primeras 4 barras)", df, "15m", skip=4, slip=False)
    run("1c. Sin look-ahead + exit slippage", df, "15m", skip=4, slip=True)
    run("1d. LONG-only (debe coincidir con 1c)", df, "15m", skip=4, direction="long", slip=True)
    run("1e. SHORT-only (test de simetría)", df, "15m", skip=4, direction="short", slip=True)

    print("\n[GRUPO 2: Bear markets - ¿funciona cuando QQQ cae?]")
    for start, end, name in [
        ("2000-01-01","2002-12-31","Dot-com 2000-2002"),
        ("2007-10-01","2009-03-31","GFC 2007-2009"),
        ("2022-01-01","2022-12-31","Bear market 2022"),
        ("2018-10-01","2018-12-31","Q4 2018"),
        ("2020-02-15","2020-03-23","Crash COVID"),
    ]:
        df = download("QQQ", "1d", start=start, end=end)
        run(f"2. {name}", df, "1d", skip=0, slip=True, max_bars=5)

    print("\n[GRUPO 3: Otros activos - ¿es QQQ-specific?]")
    for tk in ["SPY","IWM","AAPL","MSFT","TSLA","NVDA","GOOGL","META"]:
        df = download(tk, "1d")
        run(f"3. {tk} 1d", df, "1d", skip=0, slip=True, max_bars=5)

    print("\n" + "="*82)
    print("  INTERPRETACIÓN")
    print("="*82)
    print("  ✅ Si 1c > 1.5 Sharpe Y 1e (SHORT) > 0.5 Sharpe → edge simétrico, real")
    print("  ⚠️  Si 1c cae <1 vs 1a → el edge ERA look-ahead, no alpha")
    print("  ⚠️  Si 1e SHORT pierde pero 1d LONG gana → es LONG-BIAS (beta de QQQ)")
    print("  ❌ Si Grupo 2 pierde en 2000-02 y 2022 → solo gana en bull market")
    print("  ❌ Si Grupo 3 pierde en >4 de 8 tickers → es QQQ-specific, no alpha")

if __name__ == "__main__": main()