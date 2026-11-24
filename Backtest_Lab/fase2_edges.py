"""FASE 2: Validacion de 4 edges en QQQ diario + conversion a MNQ (FIX v2)"""
import os, warnings
import numpy as np, pandas as pd, yfinance as yf
from scipy import stats
warnings.filterwarnings("ignore")
CACHE_DIR = "data_cache"; os.makedirs(CACHE_DIR, exist_ok=True)

# ---------- Utilidades ----------
def to_date_df(df):
    """Garantiza que la columna de fecha se llame 'date'."""
    df = df.copy().reset_index()
    if "date" not in df.columns:
        df.rename(columns={df.columns[0]: "date"}, inplace=True)
    return df

def download(symbol, interval="1d", start=None, end=None):
    key = f"{symbol.replace('=','_')}_{interval}_{start or 'max'}_{end or 'now'}"
    cache = os.path.join(CACHE_DIR, key + ".csv")
    if os.path.exists(cache):
        return pd.read_csv(cache, index_col=0, parse_dates=True)
    print(f"  [desc] {symbol} {interval} {start or ''}->{end or ''}...", end=" ")
    kw = dict(interval=interval, progress=False, auto_adjust=False, threads=False)
    df = yf.download(symbol, start=start, end=end, **kw) if start else yf.download(symbol, period="max", **kw)
    if df is None or df.empty: print("vacio"); return pd.DataFrame()
    if isinstance(df.columns, pd.MultiIndex): df.columns = df.columns.get_level_values(0)
    df.columns = [c.lower() for c in df.columns]
    df = df[["open","high","low","close","volume"]].dropna()
    df.index.name = "date"   # <-- fix: forzar nombre del indice
    df.to_csv(cache); print(f"{len(df)} velas"); return df

def metrics_from_equity(eq, capital):
    daily_ret = eq.pct_change().dropna()
    if len(daily_ret) < 20: return None
    years = (eq.index[-1] - eq.index[0]).days / 365.25
    cagr = (eq.iloc[-1]/eq.iloc[0])**(1/years) - 1 if years > 0 else 0
    sr = daily_ret.mean() / daily_ret.std() * np.sqrt(252) if daily_ret.std() > 0 else 0
    dn = daily_ret[daily_ret < 0]
    sortino = daily_ret.mean() / dn.std() * np.sqrt(252) if len(dn) > 0 and dn.std() > 0 else 0
    peak = eq.cummax(); dd = (eq - peak) / peak
    return {"sharpe": sr, "sortino": sortino, "cagr": cagr*100, "mdd": dd.min()*100, "final_nav": eq.iloc[-1]}

# ---------- Edge 1: TSMOM 12-1 ----------
def backtest_tsmom(df, capital=10000, lookback=252, hold=21, risk_pct=1.0):
    df = to_date_df(df)
    df["ret"] = df["close"].pct_change()
    df["mom"] = df["close"].pct_change(lookback).shift(hold)
    df["vol"] = df["ret"].rolling(20).std() * np.sqrt(252)
    nav = capital; equity = []
    for i in range(lookback + hold, len(df), hold):
        row = df.iloc[i]
        if pd.isna(row["mom"]) or pd.isna(row["vol"]) or row["vol"] <= 0: continue
        size = min(risk_pct/100 / row["vol"], 0.15)
        pos = 1 if row["mom"] > 0 else -1
        ret_next = df["ret"].iloc[i:i+hold].sum()
        nav += nav * size * pos * ret_next
        equity.append({"date": row["date"], "nav": nav})
    if len(equity) < 5: return None, pd.Series()
    eq = pd.Series([e["nav"] for e in equity], index=[e["date"] for e in equity])
    return metrics_from_equity(eq, capital), eq

# ---------- Edge 2: Pairs Trading QQQ vs SPY ----------
def backtest_pairs(df_qqq, df_spy, capital=10000, window=20, entry_z=1.5, exit_z=0.5):
    q = df_qqq["close"].reindex(df_spy.index).dropna()
    s = df_spy["close"].reindex(q.index).dropna()
    ratio = s / q
    z = (ratio - ratio.rolling(window).mean()) / ratio.rolling(window).std()
    nav = capital; equity = []; pos = 0
    for i in range(window, len(z)):
        zi = z.iloc[i]
        if pd.isna(zi): continue
        if pos == 0:
            if zi > entry_z: pos = -1
            elif zi < -entry_z: pos = 1
        elif (pos == 1 and zi > -exit_z) or (pos == -1 and zi < exit_z):
            pos = 0
        if pos != 0 and i > 0:
            ret_q = q.pct_change().iloc[i]
            ret_s = s.pct_change().iloc[i]
            pnl = pos * (ret_s - ret_q) * 0.3
            nav *= (1 + pnl)
        equity.append({"date": z.index[i], "nav": nav})
    if len(equity) < 5: return None, pd.Series()
    eq = pd.Series([e["nav"] for e in equity], index=[e["date"] for e in equity])
    return metrics_from_equity(eq, capital), eq

# ---------- Edge 3: Shannon Entropy Breakout ----------
def entropy(ret, bins=10):
    if len(ret) < 2: return 0
    hist, _ = np.histogram(ret, bins=bins)
    p = hist / hist.sum()
    p = p[p > 0]
    return -np.sum(p * np.log2(p))

def backtest_entropy(df, capital=10000, window=20, risk_pct=1.0):
    df = to_date_df(df)
    df["ret"] = df["close"].pct_change()
    df["ent"] = df["ret"].rolling(window).apply(lambda x: entropy(x), raw=True)
    df["ema9"] = df["close"].ewm(span=9).mean()
    df["ema21"] = df["close"].ewm(span=21).mean()
    nav = capital; equity = []; pos = 0
    for i in range(window, len(df)):
        row = df.iloc[i]
        if pd.isna(row["ent"]): continue
        ent_thresh = df["ent"].iloc[max(0,i-100):i].quantile(0.2)
        if pos == 0:
            if row["ent"] < ent_thresh and row["ema9"] > row["ema21"]: pos = 1
            elif row["ent"] < ent_thresh and row["ema9"] < row["ema21"]: pos = -1
        else:
            if (pos == 1 and row["ema9"] < row["ema21"]) or (pos == -1 and row["ema9"] > row["ema21"]):
                pos = 0
        if pos != 0 and i+1 < len(df):
            ret_next = df["ret"].iloc[i+1]
            nav += nav * (risk_pct/100) * pos * ret_next
        equity.append({"date": row["date"], "nav": nav})
    if len(equity) < 5: return None, pd.Series()
    eq = pd.Series([e["nav"] for e in equity], index=[e["date"] for e in equity])
    return metrics_from_equity(eq, capital), eq

# ---------- Edge 4: 8am Reversion (adaptada a diario) ----------
def backtest_8am(df, capital=10000, risk_pct=1.0):
    df = to_date_df(df)
    nav = capital; equity = []; pos = 0; entry_px = 0; stop = 0; target = 0
    for i in range(1, len(df)):
        row = df.iloc[i]; prev = df.iloc[i-1]
        rng_high = prev["high"]; rng_low = prev["low"]
        rng = rng_high - rng_low
        if pos != 0:
            exit_px = None
            if pos == 1:
                if row["low"] <= stop: exit_px = stop
                elif row["high"] >= target: exit_px = target
            else:
                if row["high"] >= stop: exit_px = stop
                elif row["low"] <= target: exit_px = target
            if exit_px is not None:
                ret = (exit_px - entry_px)/entry_px if pos == 1 else (entry_px - exit_px)/entry_px
                nav += nav * (risk_pct/100) * ret
                pos = 0
        elif rng > 0:
            if row["close"] > rng_high:
                pos = -1; entry_px = rng_high; stop = rng_high + rng*1.25; target = rng_high - rng*1.5
            elif row["close"] < rng_low:
                pos = 1; entry_px = rng_low; stop = rng_low - rng*1.25; target = rng_low + rng*1.5
        equity.append({"date": row["date"], "nav": nav})
    if len(equity) < 5: return None, pd.Series()
    eq = pd.Series([e["nav"] for e in equity], index=[e["date"] for e in equity])
    return metrics_from_equity(eq, capital), eq

# ---------- Test corto en MNQ ----------
def test_mnq(capital=10000):
    print("\n[TEST CORTO MNQ - ultimos 2 anios]")
    df = download("NQ=F", "1h", start="2024-01-01")
    if df.empty: print("  Sin datos de NQ=F"); return
    df = to_date_df(df)
    df["ret"] = df["close"].pct_change()
    df["mom"] = df["close"].pct_change(252).shift(21)
    df["vol"] = df["ret"].rolling(20).std() * np.sqrt(252)
    nav = capital; equity = []
    for i in range(300, len(df), 21):
        row = df.iloc[i]
        if pd.isna(row["mom"]) or pd.isna(row["vol"]) or row["vol"] <= 0: continue
        size = min(0.01 / row["vol"], 0.15)
        pos = 1 if row["mom"] > 0 else -1
        ret_next = df["ret"].iloc[i:i+21].sum()
        nav += nav * size * pos * ret_next
        equity.append({"date": row["date"], "nav": nav})
    if len(equity) < 5: print("  Sin suficientes datos"); return
    eq = pd.Series([e["nav"] for e in equity], index=[e["date"] for e in equity])
    m = metrics_from_equity(eq, capital)
    if m:
        mnq_nav = m["final_nav"] * 0.5
        print(f"  TSMOM en NQ=F: Sharpe {m['sharpe']:.2f} | CAGR {m['cagr']:.2f}% | DD {m['mdd']:.2f}%")
        print(f"  Equivalente MNQ (estimado): ${mnq_nav:,.0f} desde $10,000")

# ---------- Main ----------
def main():
    print("="*82); print("  FASE 2: VALIDACION DE 4 EDGES EN QQQ DIARIO"); print("="*82)
    capital = 10000

    qqq = download("QQQ", "1d")
    spy = download("SPY", "1d")
    if qqq.empty or spy.empty: print("Sin datos"); return

    results = []

    print("\n[1/4] TSMOM 12-1...")
    m, _ = backtest_tsmom(qqq, capital)
    if m: results.append(("TSMOM_12_1", m)); print(f"  Sharpe {m['sharpe']:.2f} | CAGR {m['cagr']:.2f}% | DD {m['mdd']:.2f}% | NAV ${m['final_nav']:,.0f}")

    print("[2/4] Pairs Trading QQQ vs SPY...")
    m, _ = backtest_pairs(qqq, spy, capital)
    if m: results.append(("Pairs_QQQ_SPY", m)); print(f"  Sharpe {m['sharpe']:.2f} | CAGR {m['cagr']:.2f}% | DD {m['mdd']:.2f}% | NAV ${m['final_nav']:,.0f}")

    print("[3/4] Shannon Entropy Breakout...")
    m, _ = backtest_entropy(qqq, capital)
    if m: results.append(("Shannon_Entropy", m)); print(f"  Sharpe {m['sharpe']:.2f} | CAGR {m['cagr']:.2f}% | DD {m['mdd']:.2f}% | NAV ${m['final_nav']:,.0f}")

    print("[4/4] 8am Reversion (adaptada a diario)...")
    m, _ = backtest_8am(qqq, capital)
    if m: results.append(("8am_Reversion", m)); print(f"  Sharpe {m['sharpe']:.2f} | CAGR {m['cagr']:.2f}% | DD {m['mdd']:.2f}% | NAV ${m['final_nav']:,.0f}")

    print("\n" + "="*82); print("  TABLA COMPARATIVA - QQQ DIARIO (27 anios)"); print("="*82)
    print(f"{'Edge':20s} {'Sharpe':>8s} {'CAGR%':>8s} {'MaxDD%':>8s} {'NAV final':>12s}")
    print("-"*60)
    for name, m in results:
        print(f"{name:20s} {m['sharpe']:8.2f} {m['cagr']:8.2f} {m['mdd']:8.2f} ${m['final_nav']:12,.0f}")

    test_mnq(capital)

if __name__ == "__main__": main()