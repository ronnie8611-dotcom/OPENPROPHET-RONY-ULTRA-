"""FASE 2 v4: 3 bugs corregidos - metricas honestas"""
import os, warnings
import numpy as np, pandas as pd, yfinance as yf
warnings.filterwarnings("ignore")
CACHE_DIR = "data_cache"; os.makedirs(CACHE_DIR, exist_ok=True)

def to_date_df(df):
    df = df.copy().reset_index()
    if "date" not in df.columns: df.rename(columns={df.columns[0]: "date"}, inplace=True)
    return df

def download(symbol, interval="1d"):
    key = f"{symbol.replace('=','_')}_{interval}_max_now"
    cache = os.path.join(CACHE_DIR, key + ".csv")
    if os.path.exists(cache): return pd.read_csv(cache, index_col=0, parse_dates=True)
    print(f"  [desc] {symbol}...", end=" ")
    df = yf.download(symbol, period="max", interval=interval, progress=False, auto_adjust=False, threads=False)
    if df is None or df.empty: print("vacio"); return pd.DataFrame()
    if isinstance(df.columns, pd.MultiIndex): df.columns = df.columns.get_level_values(0)
    df.columns = [c.lower() for c in df.columns]
    df = df[["open","high","low","close","volume"]].dropna()
    df.index.name = "date"; df.to_csv(cache); print(f"{len(df)} velas"); return df

# FIX #3: periods_per_year parametrizable
def metrics(eq, capital, periods_per_year=252):
    r = eq.pct_change().dropna()
    if len(r) < 10: return None
    y = (eq.index[-1] - eq.index[0]).days / 365.25
    cagr = (eq.iloc[-1]/eq.iloc[0])**(1/y) - 1 if y > 0 else 0
    sr = r.mean()/r.std()*np.sqrt(periods_per_year) if r.std() > 0 else 0
    dn = r[r < 0]
    sortino = r.mean()/dn.std()*np.sqrt(periods_per_year) if len(dn) > 0 and dn.std() > 0 else 0
    peak = eq.cummax(); dd = (eq - peak)/peak
    return {"sharpe": sr, "sortino": sortino, "cagr": cagr*100, "mdd": dd.min()*100, "final": eq.iloc[-1], "eq": eq, "n_points": len(r)}

# FIX #2: max_lev = 0.30 (no 0.95)
def backtest_tsmom(df, capital=10000, lookback=252, hold=21, target_vol=0.15, max_lev=0.30):
    df = to_date_df(df)
    df["ret"] = df["close"].pct_change()
    df["mom"] = df["close"].pct_change(lookback).shift(hold)
    df["vol"] = df["ret"].rolling(20).std() * np.sqrt(252)
    nav = capital; equity = []
    for i in range(lookback+hold, len(df), hold):
        row = df.iloc[i]
        if pd.isna(row["mom"]) or pd.isna(row["vol"]) or row["vol"] <= 0: continue
        size = min(target_vol / row["vol"], max_lev)  # Cap al 30% NAV
        pos = 1 if row["mom"] > 0 else -1
        ret_next = df["ret"].iloc[i:i+hold].sum()
        nav += nav * size * pos * ret_next
        equity.append({"date": row["date"], "nav": nav})
    if len(equity) < 10: return None, pd.Series()
    eq = pd.Series([e["nav"] for e in equity], index=[e["date"] for e in equity])
    # FIX #3: TSMOM opera mensual -> sqrt(12)
    return metrics(eq, capital, periods_per_year=12), eq

# FIX #1: SL/TP correcto (ih-il, no il-il)
def backtest_raschke(df, capital=10000, risk_pct=1.0, comm=0.005, slip_bps=2.0, max_bars=5):
    df = to_date_df(df)
    df["ib_high"] = df["high"].shift(1); df["ib_low"] = df["low"].shift(1)
    df["ib_mid"] = (df["ib_high"]+df["ib_low"])/2
    sigs = []
    for i in range(1, len(df)):
        r = df.iloc[i]
        if pd.isna(r["ib_high"]): continue
        p, ih, il, im = r["close"], r["ib_high"], r["ib_low"], r["ib_mid"]
        if p >= ih*0.999:
            sigs.append({"idx": i, "dir": 1, "entry": ih, "sl": im, "tp": ih+(ih-il)*0.5})
        elif p <= il*1.001:
            # FIX #1: era (il-il), ahora (ih-il)
            sigs.append({"idx": i, "dir": -1, "entry": il, "sl": im, "tp": il-(ih-il)*0.5})
    nav = capital; equity = []; open_t = None
    sig_by_day = {}
    for s in sigs: sig_by_day.setdefault(s["idx"], []).append(s)
    for i in range(len(df)):
        r = df.iloc[i]
        if open_t is not None:
            t = open_t; exit_px = None
            if t["dir"] == 1:
                if r["low"] <= t["sl"]: exit_px = t["sl"]
                elif r["high"] >= t["tp"]: exit_px = t["tp"]
            else:
                if r["high"] >= t["sl"]: exit_px = t["sl"]
                elif r["low"] <= t["tp"]: exit_px = t["tp"]
            if exit_px is None and (i-t["entry_idx"]) >= max_bars: exit_px = r["close"]
            if exit_px is not None:
                ex = exit_px * (1 - slip_bps/10000 * t["dir"])
                pnl = (ex - t["entry_px"]) * t["dir"] * t["qty"] - comm*t["qty"]*2
                nav += pnl; open_t = None
        if open_t is None and i in sig_by_day:
            s = sig_by_day[i][0]
            entry_px = s["entry"] * (1 + slip_bps/10000 * s["dir"])
            risk_dollars = nav * risk_pct/100
            sl_dist = abs(entry_px - s["sl"])
            if sl_dist > 0:
                qty = int(min(risk_dollars/sl_dist, (nav*0.15)/entry_px))
                if qty >= 1:
                    open_t = {"dir": s["dir"], "entry_px": entry_px, "sl": s["sl"], "tp": s["tp"], "qty": qty, "entry_idx": i}
        equity.append({"date": df.iloc[i]["date"], "nav": nav})
    if len(equity) < 10: return None, pd.Series()
    eq = pd.Series([e["nav"] for e in equity], index=[e["date"] for e in equity])
    # Raschke tradea diario -> sqrt(252)
    return metrics(eq, capital, periods_per_year=252), eq

def main():
    print("="*82); print("  FASE 2 v4: 3 bugs corregidos - metricas honestas"); print("="*82)
    qqq = download("QQQ", "1d")
    if qqq.empty: print("Sin datos"); return

    print("\n[Raschke - con SL/TP correcto]")
    m_r, eq_r = backtest_raschke(qqq, capital=10000)
    if m_r:
        print(f"  Sharpe {m_r['sharpe']:.2f} | CAGR {m_r['cagr']:.2f}% | DD {m_r['mdd']:.2f}% | NAV ${m_r['final']:,.0f}")

    print("\n[TSMOM - max_lev=30% NAV, Sharpe corregido (sqrt(12))]")
    m_t, eq_t = backtest_tsmom(qqq, capital=10000, max_lev=0.30)
    if m_t:
        print(f"  Sharpe {m_t['sharpe']:.2f} | CAGR {m_t['cagr']:.2f}% | DD {m_t['mdd']:.2f}% | NAV ${m_t['final']:,.0f}")

    print("\n[TSMOM max_lev=50% - alternativa mas agresiva]")
    m_t2, eq_t2 = backtest_tsmom(qqq, capital=10000, max_lev=0.50)
    if m_t2:
        print(f"  Sharpe {m_t2['sharpe']:.2f} | CAGR {m_t2['cagr']:.2f}% | DD {m_t2['mdd']:.2f}% | NAV ${m_t2['final']:,.0f}")

    # Portfolio
    if m_r and m_t:
        print("\n[Portfolio 50/50: Raschke + TSMOM(lev=30%)]")
        common = eq_r.index.intersection(eq_t.index)
        if len(common) > 30:
            r1 = eq_r.reindex(common).pct_change().fillna(0)
            r2 = eq_t.reindex(common).pct_change().fillna(0)
            r_port = 0.5*r1 + 0.5*r2
            eq_port = (1 + r_port).cumprod() * 10000
            m_port = metrics(eq_port, 10000, periods_per_year=252)
            if m_port:
                print(f"  Sharpe {m_port['sharpe']:.2f} | CAGR {m_port['cagr']:.2f}% | DD {m_port['mdd']:.2f}% | NAV ${m_port['final']:,.0f}")
                print(f"  Correlacion Raschke/TSMOM: {r1.corr(r2):.2f}")

    print("\n" + "="*82)
    print("  INTERPRETACION ESPERADA")
    print("="*82)
    print("  Raschke debe recuperar Sharpe ~3.48 (como backtest_correcto.py)")
    print("  TSMOM deberia dar Sharpe ~0.35-0.50 (honesto, no inflado)")
    print("  Si TSMOM < 0.5 -> no vale la pena, usar solo Raschke")
    print("  Portfolio: si Sharpe > 2.5 y DD < 5% -> deploy viable")

if __name__ == "__main__": main()